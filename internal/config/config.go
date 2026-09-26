// Package config loads versioned YAML plus defaults.
// Secrets belong in the OS keychain (encrypted file fallback) — not this file.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

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

// LoadOrDefault loads path, or DefaultPath if empty. Missing files yield Default().
func LoadOrDefault(path string) (Config, string, error) {
	if path == "" {
		path = DefaultPath()
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Default(), path, nil
		}
		return Config{}, path, err
	}
	cfg, err := Load(path)
	return cfg, path, err
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
	ip := net.ParseIP(c.Bind)
	loopback := c.Bind == "127.0.0.1" || c.Bind == "localhost" || (ip != nil && ip.IsLoopback())
	if !loopback {
		if !c.AllowNonLoopback || c.AdminToken == "" {
			return fmt.Errorf("non-loopback bind %q requires allowNonLoopback: true and a non-empty adminToken", c.Bind)
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
