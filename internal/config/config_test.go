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
	cfg.Providers[0].APIKey = "sk-test"
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	again, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Providers[0].APIKey != "sk-test" {
		t.Fatalf("save lost apiKey")
	}
}

func TestOAuthTokenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.Provider{
		ID:      "anthropic-oauth",
		Adapter: "anthropic_oauth",
		Tier:    "paid",
		OAuth: &config.OAuthToken{
			AccessToken:  "at",
			RefreshToken: "rt",
			ExpiresAt:    "2026-09-26T12:00:00Z",
			Email:        "a@b.c",
		},
	})
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	again, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var found *config.OAuthToken
	for _, p := range again.Providers {
		if p.ID == "anthropic-oauth" {
			found = p.OAuth
		}
	}
	if found == nil || found.AccessToken != "at" || found.RefreshToken != "rt" || found.Email != "a@b.c" {
		t.Fatalf("%#v", found)
	}
	if !found.Runtime().Valid() {
		t.Fatal("runtime token should be valid")
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
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when bind-all has token but no --allow-lan")
	}
	cfg.AllowNonLoopback = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureFileWritesOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	cfg, gotPath, created, err := config.EnsureFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created || gotPath != path {
		t.Fatalf("created=%v path=%s", created, gotPath)
	}
	if cfg.Bind != "127.0.0.1" || len(cfg.Providers) != 1 {
		t.Fatalf("%#v", cfg)
	}
	again, _, created2, err := config.EnsureFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatal("second EnsureFile must not rewrite")
	}
	if again.Providers[0].ID != "ollama-local" {
		t.Fatalf("%#v", again.Providers)
	}
}

func TestApplyEnvOverlaysFile(t *testing.T) {
	cfg := config.Default()
	t.Setenv("PEAPROXY_BIND", "127.0.0.1")
	t.Setenv("PEAPROXY_PORT", "9001")
	t.Setenv("PEAPROXY_ADMIN_TOKEN", "secret-token")
	t.Setenv("PEAPROXY_ALLOW_LAN", "true")
	t.Setenv("PEAPROXY_REQUEST_LOG", "1")
	config.ApplyEnv(&cfg)
	if cfg.Port != 9001 || cfg.AdminToken != "secret-token" || !cfg.AllowNonLoopback || !cfg.RequestLog {
		t.Fatalf("%#v", cfg)
	}
}

func TestIsLoopback(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "localhost", "::1"} {
		if !config.IsLoopback(h) {
			t.Fatalf("%s should be loopback", h)
		}
	}
	if config.IsLoopback("0.0.0.0") || config.IsLoopback("192.168.1.5") {
		t.Fatal("non-loopback reported as loopback")
	}
}
