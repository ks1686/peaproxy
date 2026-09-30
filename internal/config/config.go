// Package config loads versioned YAML plus defaults.
// OAuth tokens and inline API keys are stored via internal/secretstore
// (OS keychain, or an AES-GCM file next to this YAML). The YAML still lists
// accounts and non-secret OAuth metadata (email, expiry).
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/fslock"
	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/ks1686/peaproxy/internal/router"
	"gopkg.in/yaml.v3"
)

const (
	SchemaVersion = 1
	DefaultBind   = "127.0.0.1"
	DefaultPort   = 8317
)

// Config is the on-disk schema. schemaVersion must be bumped on breaking changes.
type Config struct {
	SchemaVersion    int                 `yaml:"schemaVersion"`
	Bind             string              `yaml:"bind"`
	Port             int                 `yaml:"port"`
	AdminToken       string              `yaml:"adminToken,omitempty"`
	AllowNonLoopback bool                `yaml:"allowNonLoopback,omitempty"`
	RequestLog       bool                `yaml:"requestLog,omitempty"`
	Hide             HideList            `yaml:"hide"`
	Expose           ExposeList          `yaml:"expose"`
	Catalog          CatalogPrefs        `yaml:"catalog,omitempty"`
	Failover         FailoverPrefs       `yaml:"failover,omitempty"`
	RequestEngine    RequestEnginePrefs  `yaml:"requestEngine,omitempty"`
	AutomaticRoutes  AutomaticRoutePrefs `yaml:"automaticRoutes,omitempty"`
	Routes           map[string]string   `yaml:"routes,omitempty"`
	Providers        []Provider          `yaml:"providers"`
}

// HideList drops providers or model IDs from /v1/models and UI pickers.
// Routing is unchanged unless BlockRouting is true (CPA #5995).
type HideList struct {
	Providers    []string `yaml:"providers" json:"providers"`
	Models       []string `yaml:"models" json:"models"`
	BlockRouting bool     `yaml:"blockRouting,omitempty" json:"blockRouting,omitempty"`
}

// ExposeList is the optional subset coding tools see. Empty = all non-hidden.
type ExposeList struct {
	Models []string `yaml:"models" json:"models"`
}

// CatalogPrefs are optional UI overlays. Live ListModels remains the source of IDs.
// Rename/pin never affect routing unless hide.blockRouting is set.
type CatalogPrefs struct {
	Pin    []string          `yaml:"pin,omitempty" json:"pin,omitempty"`
	Rename map[string]string `yaml:"rename,omitempty" json:"rename,omitempty"`
}

// FailoverPrefs selects how matching accounts are ordered. Empty policy is round-robin.
// Session affinity is on unless sessionAffinity is false. It pins one conversation
// to one account until the TTL, then fails over when that account is cooled.
type FailoverPrefs struct {
	Policy             string `yaml:"policy,omitempty" json:"policy,omitempty"`
	SessionAffinity    *bool  `yaml:"sessionAffinity,omitempty" json:"sessionAffinity,omitempty"`
	SessionAffinityTTL string `yaml:"sessionAffinityTTL,omitempty" json:"sessionAffinityTTL,omitempty"`
}

// AffinityEnabled reports whether a conversation should stay on one account.
// An omitted sessionAffinity field means on.
func (c Config) AffinityEnabled() bool {
	if c.Failover.SessionAffinity == nil {
		return true
	}
	return *c.Failover.SessionAffinity
}

// AffinityTTL is how long a conversation stays pinned. Empty or invalid is 1h.
func (c Config) AffinityTTL() time.Duration {
	s := strings.TrimSpace(c.Failover.SessionAffinityTTL)
	if s == "" {
		return time.Hour
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return time.Hour
	}
	return d
}

// FailoverPolicy returns the effective routing policy (round-robin by default).
func (c Config) FailoverPolicy() string {
	switch strings.ToLower(strings.TrimSpace(c.Failover.Policy)) {
	case "fill-first":
		return "fill-first"
	case "sticky":
		return "sticky"
	case "adaptive":
		return "adaptive"
	default:
		return "round-robin"
	}
}

// Provider is one adapter instance (Ollama, a key, or an OAuth account stub).
type Provider struct {
	ID      string `yaml:"id"`
	Adapter string `yaml:"adapter"`
	Tier    string `yaml:"tier"`
	BaseURL string `yaml:"baseURL,omitempty"`
	// APIKeyEnv names an env var; preferred over apiKey when set.
	APIKeyEnv string `yaml:"apiKeyEnv,omitempty"`
	// APIKey is an inline key. Save persists it in the secret store, not YAML.
	// Prefer APIKeyEnv.
	APIKey string `yaml:"apiKey,omitempty"`
	// SessionID is used by OpenCode Zen (x-session-id). Fragile vs API key.
	SessionID string `yaml:"sessionId,omitempty"`
	// Label is a user-facing tier override (free|freemium|paid|local).
	Label    string `yaml:"label,omitempty"`
	Disabled bool   `yaml:"disabled,omitempty"`
	// OAuth holds subscription tokens in memory. Save writes tokens to the
	// secret store and keeps only non-secret metadata in YAML.
	OAuth *OAuthToken `yaml:"oauth,omitempty"`
}

// OAuthToken is persisted next to the provider. Never log these fields.
type OAuthToken struct {
	AccessToken  string `yaml:"accessToken,omitempty"`
	RefreshToken string `yaml:"refreshToken,omitempty"`
	ExpiresAt    string `yaml:"expiresAt,omitempty"`
	IDToken      string `yaml:"idToken,omitempty"`
	AccountID    string `yaml:"accountId,omitempty"`
	Email        string `yaml:"email,omitempty"`
	PlanType     string `yaml:"planType,omitempty"`
	// Extra is provider-specific (project_id, device_id). May contain secrets.
	Extra map[string]string `yaml:"extra,omitempty"`
}

// ResolveKey returns the API key from env or the inline field.
func (p Provider) ResolveKey() string {
	if p.APIKeyEnv != "" {
		if v := os.Getenv(p.APIKeyEnv); v != "" {
			return v
		}
	}
	return p.APIKey
}

// HasOAuth reports whether subscription tokens are stored for this account.
func (p Provider) HasOAuth() bool {
	return p.OAuth != nil && p.OAuth.AccessToken != ""
}

// Runtime converts persisted YAML tokens into the in-memory shape.
func (t OAuthToken) Runtime() oauth.Token {
	tok := oauth.Token{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		IDToken:      t.IDToken,
		AccountID:    t.AccountID,
		Email:        t.Email,
		PlanType:     t.PlanType,
		Extra:        t.Extra,
	}
	if t.ExpiresAt != "" {
		if ts, err := time.Parse(time.RFC3339, t.ExpiresAt); err == nil {
			tok.ExpiresAt = ts
		}
	}
	return tok
}

// OAuthFromRuntime persists an in-memory token.
func OAuthFromRuntime(tok oauth.Token) OAuthToken {
	out := OAuthToken{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		IDToken:      tok.IDToken,
		AccountID:    tok.AccountID,
		Email:        tok.Email,
		PlanType:     tok.PlanType,
		Extra:        tok.Extra,
	}
	if !tok.ExpiresAt.IsZero() {
		out.ExpiresAt = tok.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out
}

// Default returns a loopback-only skeleton config with local Ollama.
func Default() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Bind:          DefaultBind,
		Port:          DefaultPort,
		Providers: []Provider{
			{
				ID:      "ollama-local",
				Adapter: "ollama",
				Tier:    "local",
				BaseURL: "http://127.0.0.1:11434/v1",
			},
		},
	}
}

// DefaultPath is ~/.config/peaproxy/config.yaml (or %AppData% on Windows).
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "peaproxy.yaml"
	}
	return filepath.Join(dir, "peaproxy", "config.yaml")
}

// LoadOrDefault loads path, or DefaultPath if empty. Missing files yield Default()
// without writing. Serve calls EnsureFile to persist a first-run skeleton.
func LoadOrDefault(path string) (Config, string, error) {
	if path == "" {
		path = DefaultPath()
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			cfg := Default()
			ApplyEnv(&cfg)
			return cfg, path, nil
		}
		return Config{}, path, err
	}
	cfg, err := Load(path)
	if err != nil {
		return Config{}, path, err
	}
	ApplyEnv(&cfg)
	return cfg, path, nil
}

// EnsureFile writes Default() to path when the file does not exist, then
// overlays PEAPROXY_* env on the returned config (never on the file). An
// existing file is read without config.lock, so a read-only config dir works.
func EnsureFile(path string) (Config, string, bool, error) {
	if path == "" {
		path = DefaultPath()
	}
	if _, err := os.Stat(path); err == nil {
		cfg, err := Load(path)
		if err != nil {
			return Config{}, path, false, err
		}
		ApplyEnv(&cfg)
		return cfg, path, false, nil
	} else if !os.IsNotExist(err) {
		return Config{}, path, false, err
	}
	unlock, err := lockConfig(path)
	if err != nil {
		return Config{}, path, false, err
	}
	cfg, created, err := ensureLocked(path)
	unlock()
	if err != nil {
		return Config{}, path, false, err
	}
	ApplyEnv(&cfg)
	return cfg, path, created, nil
}

// Update runs fn on the config at path (Default() if missing) and saves the
// result, all under config.lock so concurrent writers cannot lose each
// other's changes. Env overlays are not applied, so they are never persisted.
func Update(path string, fn func(*Config) error) (Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	unlock, err := lockConfig(path)
	if err != nil {
		return Config{}, err
	}
	defer unlock()
	cfg, _, err := ensureLocked(path)
	if err != nil {
		return Config{}, err
	}
	if err := fn(&cfg); err != nil {
		return Config{}, err
	}
	if err := saveLocked(path, cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// SaveMerged writes Merge(base, disk, mine) under config.lock, where disk is
// the file as it is now (base when missing), and returns the merged config.
// A file that exists but cannot be read is never overwritten.
func SaveMerged(path string, base, mine Config) (Config, error) {
	unlock, err := lockConfig(path)
	if err != nil {
		return Config{}, err
	}
	defer unlock()
	var disk Config
	if _, err := os.Stat(path); os.IsNotExist(err) {
		disk = base
	} else if disk, err = loadLocked(path); err != nil {
		return Config{}, err
	}
	merged := Merge(base, disk, mine)
	if err := saveLocked(path, merged); err != nil {
		return Config{}, err
	}
	return merged, nil
}

// lockTimeout bounds how long a writer waits for config.lock. Tests may shorten it.
var lockTimeout = 15 * time.Second

// lockConfig takes config.lock next to path, creating the directory first so
// a first run on a fresh machine works. fslock is not reentrant: every public
// entry point calls this exactly once and then only *Locked helpers.
// Lock order is config.lock before secrets.lock, never the reverse.
func lockConfig(path string) (func(), error) {
	dir := configDir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil && dir != "." {
		return nil, err
	}
	unlock, err := fslock.Lock(filepath.Join(dir, "config.lock"), lockTimeout)
	if err != nil {
		return nil, fmt.Errorf("config %s busy (another peaproxy process is saving): %w", dir, err)
	}
	return unlock, nil
}

// ensureLocked loads path, or saves and returns Default() when it is missing.
func ensureLocked(path string) (Config, bool, error) {
	if _, err := os.Stat(path); err == nil {
		cfg, err := loadLocked(path)
		return cfg, false, err
	} else if !os.IsNotExist(err) {
		return Config{}, false, err
	}
	cfg := Default()
	if err := saveLocked(path, cfg); err != nil {
		return Config{}, false, err
	}
	return cfg, true, nil
}

// ApplyEnv overlays PEAPROXY_* variables (file < env < CLI flags).
func ApplyEnv(c *Config) {
	if c == nil {
		return
	}
	if v := os.Getenv("PEAPROXY_BIND"); v != "" {
		c.Bind = v
	}
	if v := os.Getenv("PEAPROXY_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.Port = p
		}
	}
	if v := os.Getenv("PEAPROXY_ADMIN_TOKEN"); v != "" {
		c.AdminToken = v
	}
	if v := os.Getenv("PEAPROXY_ALLOW_LAN"); envTruthy(v) {
		c.AllowNonLoopback = true
	}
	if v := os.Getenv("PEAPROXY_REQUEST_LOG"); envTruthy(v) {
		c.RequestLog = true
	}
	if v := os.Getenv("PEAPROXY_FAILOVER_POLICY"); v != "" {
		c.Failover.Policy = v
	}
}

func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// IsLoopback reports whether bind is a loopback host.
func IsLoopback(bind string) bool {
	if bind == "127.0.0.1" || bind == "localhost" || bind == "::1" {
		return true
	}
	ip := net.ParseIP(bind)
	return ip != nil && ip.IsLoopback()
}

// Load reads YAML from path. It takes no lock: writers replace the file with
// an atomic rename, and the secret store locks itself.
func Load(path string) (Config, error) {
	return loadLocked(path)
}

func loadLocked(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg := Default()
	cfg.Providers = nil
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Bind == "" {
		cfg.Bind = DefaultBind
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = SchemaVersion
	}
	if err := hydrateSecrets(path, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, cfg.Validate()
}

// Save writes YAML with mode 0600. OAuth tokens and inline API keys go to the
// secret store (OS keychain or encrypted file); YAML keeps account metadata.
// It overwrites the whole file; writers that may race another process use
// Update or SaveMerged instead.
func Save(path string, cfg Config) error {
	unlock, err := lockConfig(path)
	if err != nil {
		return err
	}
	defer unlock()
	return saveLocked(path, cfg)
}

func saveLocked(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	dir := configDir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil && dir != "." {
		return err
	}
	disk, err := persistSecrets(path, cfg)
	if err != nil {
		return err
	}
	b, err := yaml.Marshal(disk)
	if err != nil {
		return err
	}
	return writeAtomic(path, b)
}

// writeAtomic writes b to a temp file in path's directory (mode 0600) and
// renames it over path, so readers see the old file or the new one, never a
// torn write. The temp file is removed on any failure.
func writeAtomic(path string, b []byte) (err error) {
	f, err := os.CreateTemp(configDir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return fslock.Rename(tmp, path)
}

// Validate enforces loopback-by-default security.
func (c Config) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %d (want %d)", c.SchemaVersion, SchemaVersion)
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid port %d", c.Port)
	}
	if !IsLoopback(c.Bind) {
		if !c.AllowNonLoopback || c.AdminToken == "" {
			return fmt.Errorf("non-loopback bind %q requires --allow-lan (or allowNonLoopback: true) and a non-empty adminToken", c.Bind)
		}
	}
	ids := map[string]struct{}{}
	for _, p := range c.Providers {
		if strings.TrimSpace(p.ID) == "" {
			return fmt.Errorf("provider missing id")
		}
		if strings.TrimSpace(p.Adapter) == "" {
			return fmt.Errorf("provider %q missing adapter", p.ID)
		}
		if err := validTier(p.Tier); err != nil {
			return fmt.Errorf("provider %q: %w", p.ID, err)
		}
		if _, ok := ids[p.ID]; ok {
			return fmt.Errorf("duplicate provider id %q", p.ID)
		}
		ids[p.ID] = struct{}{}
	}
	if err := requireIDs("hide.providers", c.Hide.Providers); err != nil {
		return err
	}
	if err := requireIDs("hide.models", c.Hide.Models); err != nil {
		return err
	}
	if err := requireIDs("expose.models", c.Expose.Models); err != nil {
		return err
	}
	if err := requireIDs("catalog.pin", c.Catalog.Pin); err != nil {
		return err
	}
	for id, name := range c.Catalog.Rename {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("catalog.rename has an empty model id")
		}
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("catalog.rename %q has an empty display name", id)
		}
	}
	for name, target := range c.Routes {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) || strings.ContainsAny(name, " \t\r\n") {
			return fmt.Errorf("routes name %q must be a non-empty id without spaces", name)
		}
		if strings.TrimSpace(target) == "" {
			return fmt.Errorf("routes %q has an empty target", name)
		}
		if strings.TrimSpace(target) == name {
			return fmt.Errorf("routes %q must target a different live model id", name)
		}
	}
	switch strings.ToLower(strings.TrimSpace(c.Failover.Policy)) {
	case "", "round-robin", "fill-first", "sticky", "adaptive":
	default:
		return fmt.Errorf("invalid failover.policy %q (want round-robin|fill-first|sticky|adaptive)", c.Failover.Policy)
	}
	for name := range c.Routes {
		if router.Automatic(name) {
			return fmt.Errorf("routes %q collides with an automatic route", name)
		}
	}
	switch strings.ToLower(strings.TrimSpace(c.RequestEngine.PromptCache)) {
	case "", "preserve", "optimize", "off":
	default:
		return fmt.Errorf("invalid requestEngine.promptCache %q", c.RequestEngine.PromptCache)
	}
	if s := strings.TrimSpace(c.Failover.SessionAffinityTTL); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid failover.sessionAffinityTTL %q", s)
		}
	}
	if c.RequestEngine.MaxAttempts != 0 || strings.TrimSpace(c.RequestEngine.Deadline) != "" || strings.TrimSpace(c.RequestEngine.PreludeTimeout) != "" {
		if err := c.validateRequestEngine(); err != nil {
			return err
		}
	}
	return nil
}

func validTier(tier string) error {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "", "free", "freemium", "paid", "local":
		return nil
	default:
		return fmt.Errorf("invalid tier %q (want free|freemium|paid|local)", tier)
	}
}

func requireIDs(kind string, ids []string) error {
	seen := map[string]struct{}{}
	for i, id := range ids {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%s[%d] is empty", kind, i)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate %s %q", kind, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// ValidateKnownAdapters rejects adapter names that are not in the registry.
func (c Config) ValidateKnownAdapters(known []string) error {
	set := make(map[string]struct{}, len(known))
	for _, n := range known {
		set[n] = struct{}{}
	}
	for _, p := range c.Providers {
		if p.Adapter == "" {
			continue
		}
		if _, ok := set[p.Adapter]; !ok {
			return fmt.Errorf("unknown adapter %q for provider %q (see docs/PROVIDERS.md)", p.Adapter, p.ID)
		}
	}
	return nil
}

// NeedsOnboarding is true when there are no accounts, or only the first-run
// Ollama skeleton (no key, no OAuth, not disabled).
func (c Config) NeedsOnboarding() bool {
	if len(c.Providers) == 0 {
		return true
	}
	if len(c.Providers) != 1 {
		return false
	}
	p := c.Providers[0]
	return p.ID == "ollama-local" && p.Adapter == "ollama" && p.APIKey == "" && p.APIKeyEnv == "" && !p.HasOAuth() && !p.Disabled
}

// Addr returns host:port.
func (c Config) Addr() string {
	return net.JoinHostPort(c.Bind, fmt.Sprintf("%d", c.Port))
}
