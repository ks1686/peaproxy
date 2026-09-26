// Package config loads versioned YAML plus defaults.
// Secrets belong in the OS keychain (encrypted file fallback) — not this file.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/oauth"
	"gopkg.in/yaml.v3"
)

const (
	SchemaVersion = 1
	DefaultBind   = "127.0.0.1"
	DefaultPort   = 8317
)

// Config is the on-disk schema. schemaVersion must be bumped on breaking changes.
type Config struct {
	SchemaVersion    int        `yaml:"schemaVersion"`
	Bind             string     `yaml:"bind"`
	Port             int        `yaml:"port"`
	AdminToken       string     `yaml:"adminToken,omitempty"`
	AllowNonLoopback bool       `yaml:"allowNonLoopback,omitempty"`
	RequestLog       bool       `yaml:"requestLog,omitempty"`
	Hide             HideList   `yaml:"hide"`
	Expose           ExposeList `yaml:"expose"`
	Providers        []Provider `yaml:"providers"`
}

// HideList drops providers or model IDs from /v1/models and UI pickers.
// Routing is unchanged unless BlockRouting is true (CPA #5995).
type HideList struct {
	Providers    []string `yaml:"providers"`
	Models       []string `yaml:"models"`
	BlockRouting bool     `yaml:"blockRouting,omitempty"`
}

// ExposeList is the optional subset coding tools see. Empty = all non-hidden.
type ExposeList struct {
	Models []string `yaml:"models"`
}

// Provider is one adapter instance (Ollama, a key, or an OAuth account stub).
type Provider struct {
	ID      string `yaml:"id"`
	Adapter string `yaml:"adapter"`
	Tier    string `yaml:"tier"`
	BaseURL string `yaml:"baseURL,omitempty"`
	// APIKeyEnv names an env var; preferred over apiKey when set.
	APIKeyEnv string `yaml:"apiKeyEnv,omitempty"`
	// APIKey may be stored locally (file mode 0600). Prefer APIKeyEnv.
	APIKey string `yaml:"apiKey,omitempty"`
	// SessionID is used by OpenCode Zen (x-session-id). Fragile vs API key.
	SessionID string `yaml:"sessionId,omitempty"`
	// Label is a user-facing tier override (free|freemium|paid|local).
	Label    string `yaml:"label,omitempty"`
	Disabled bool   `yaml:"disabled,omitempty"`
	// OAuth holds subscription tokens (file mode 0600). Prefer env/API keys.
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

// EnsureFile writes Default() to path when the file does not exist.
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
	cfg := Default()
	if err := Save(path, cfg); err != nil {
		return Config{}, path, false, err
	}
	ApplyEnv(&cfg)
	return cfg, path, true, nil
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

// Load reads YAML from path.
func Load(path string) (Config, error) {
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
	return cfg, cfg.Validate()
}

// Save writes YAML with mode 0600.
func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && filepath.Dir(path) != "." {
		return err
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
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
		if p.ID == "" {
			return fmt.Errorf("provider missing id")
		}
		if _, ok := ids[p.ID]; ok {
			return fmt.Errorf("duplicate provider id %q", p.ID)
		}
		ids[p.ID] = struct{}{}
	}
	return nil
}

// Addr returns host:port.
func (c Config) Addr() string {
	return net.JoinHostPort(c.Bind, fmt.Sprintf("%d", c.Port))
}
