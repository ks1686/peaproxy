package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ks1686/peaproxy/internal/config"
)

func TestDefaultsAreLoopback8317(t *testing.T) {
	cfg := config.Default()
	if cfg.SchemaVersion != 1 {
		t.Fatalf("schemaVersion: %d", cfg.SchemaVersion)
	}
	if cfg.Bind != "127.0.0.1" {
		t.Fatalf("default bind must be loopback, got %q", cfg.Bind)
	}
	if cfg.Port != 8317 {
		t.Fatalf("default port: %d", cfg.Port)
	}
}

func TestLoadYAMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
hide:
  providers:
    - anthropic
  models:
    - gpt-4o
expose:
  models:
    - llama3.2
providers:
  - id: ollama-local
    adapter: ollama
    tier: local
    baseURL: http://127.0.0.1:11434/v1
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hide.Providers[0] != "anthropic" {
		t.Fatalf("hide providers: %#v", cfg.Hide.Providers)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Adapter != "ollama" {
		t.Fatalf("providers: %#v", cfg.Providers)
	}
}

func TestExampleYAMLLoads(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "peaproxy.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bind != "127.0.0.1" || cfg.Port != 8317 {
		t.Fatalf("example defaults: bind=%s port=%d", cfg.Bind, cfg.Port)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Adapter != "ollama" {
		t.Fatalf("example providers: %#v", cfg.Providers)
	}
}

func TestBindAllRequiresExplicitOptIn(t *testing.T) {
	cfg := config.Default()
	cfg.Bind = "0.0.0.0"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when bind-all has no admin token")
	}
	cfg.AdminToken = "test-token"
	cfg.AllowNonLoopback = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
