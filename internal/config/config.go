// Package config loads versioned YAML plus defaults.
// Secrets belong in the OS keychain (encrypted file fallback) — not this file.
package config

import (
	"fmt"
	"net"
	"os"

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
	Hide             HideList   `yaml:"hide"`
	Expose           ExposeList `yaml:"expose"`
	Providers        []Provider `yaml:"providers"`
}

// HideList drops providers or model IDs from /v1/models and UI pickers.
type HideList struct {
	Providers []string `yaml:"providers"`
	Models    []string `yaml:"models"`
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
	// APIKeyEnv names an env var; the value is never written back to YAML.
	APIKeyEnv string `yaml:"apiKeyEnv,omitempty"`
	// Label is a user-facing tier override (free|freemium|paid|local).
	Label string `yaml:"label,omitempty"`
}

// Default returns a loopback-only skeleton config.
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
	return nil
}

// Addr returns host:port.
func (c Config) Addr() string {
	return net.JoinHostPort(c.Bind, fmt.Sprintf("%d", c.Port))
}
