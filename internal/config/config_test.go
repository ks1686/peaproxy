package config_test

import (
	"os"
	"path/filepath"
	"strings"
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
catalog:
  pin:
    - llama3.2
  rename:
    llama3.2: Llama 3.2 local
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
	if len(cfg.Catalog.Pin) != 1 || cfg.Catalog.Pin[0] != "llama3.2" {
		t.Fatalf("catalog pin: %#v", cfg.Catalog.Pin)
	}
	if cfg.Catalog.Rename["llama3.2"] != "Llama 3.2 local" {
		t.Fatalf("catalog rename: %#v", cfg.Catalog.Rename)
	}
	cfg.Providers[0].APIKey = "sk-test-secret-value"
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), "sk-test-secret-value") {
		t.Fatalf("apiKey leaked into YAML:\n%s", onDisk)
	}
	if !strings.Contains(string(onDisk), "Llama 3.2 local") || !strings.Contains(string(onDisk), "pin:") {
		t.Fatalf("catalog prefs missing from YAML:\n%s", onDisk)
	}
	again, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Providers[0].APIKey != "sk-test-secret-value" {
		t.Fatalf("save lost apiKey")
	}
	if again.Catalog.Rename["llama3.2"] != "Llama 3.2 local" {
		t.Fatalf("save lost rename: %#v", again.Catalog)
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
			AccessToken:  "access-token-secret",
			RefreshToken: "refresh-token-secret",
			ExpiresAt:    "2026-09-26T12:00:00Z",
			Email:        "a@b.c",
			Extra:        map[string]string{"project_id": "proj-1", "dca_token": "dca-secret"},
		},
	})
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(onDisk)
	for _, secret := range []string{"access-token-secret", "refresh-token-secret", "dca-secret"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("oauth secret %q leaked into YAML:\n%s", secret, raw)
		}
	}
	if !strings.Contains(raw, "a@b.c") || !strings.Contains(raw, "proj-1") {
		t.Fatalf("public oauth metadata missing from YAML:\n%s", raw)
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
	if found == nil || found.AccessToken != "access-token-secret" || found.RefreshToken != "refresh-token-secret" || found.Email != "a@b.c" {
		t.Fatalf("%#v", found)
	}
	if found.Extra["project_id"] != "proj-1" {
		t.Fatalf("extra %#v", found.Extra)
	}
	if found.Extra["dca_token"] != "dca-secret" {
		t.Fatalf("dca_token not hydrated: %#v", found.Extra)
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
	if cfg.FailoverPolicy() != "round-robin" {
		t.Fatalf("example failover.policy %q", cfg.FailoverPolicy())
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

func TestLegacyYAMLSecretsMigrateOnSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: anthropic-key
    adapter: anthropic
    tier: paid
    apiKey: sk-legacy-inline
  - id: anthropic-oauth
    adapter: anthropic_oauth
    tier: paid
    oauth:
      accessToken: legacy-access
      refreshToken: legacy-refresh
      email: old@example.com
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers[0].APIKey != "sk-legacy-inline" {
		t.Fatalf("legacy apiKey: %#v", cfg.Providers[0])
	}
	if !cfg.Providers[1].HasOAuth() || cfg.Providers[1].OAuth.AccessToken != "legacy-access" {
		t.Fatalf("legacy oauth: %#v", cfg.Providers[1].OAuth)
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(onDisk)
	for _, secret := range []string{"sk-legacy-inline", "legacy-access", "legacy-refresh"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("legacy secret %q still in YAML:\n%s", secret, raw)
		}
	}
	if !strings.Contains(raw, "old@example.com") {
		t.Fatalf("email should remain in YAML:\n%s", raw)
	}
	again, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Providers[0].APIKey != "sk-legacy-inline" {
		t.Fatalf("migrated apiKey lost: %#v", again.Providers[0])
	}
	if again.Providers[1].OAuth == nil || again.Providers[1].OAuth.AccessToken != "legacy-access" {
		t.Fatalf("migrated oauth lost: %#v", again.Providers[1].OAuth)
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

func TestFailoverPolicyLoadAndDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
failover:
  policy: fill-first
providers:
  - id: ollama-local
    adapter: ollama
    tier: local
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FailoverPolicy() != "fill-first" {
		t.Fatalf("policy %q", cfg.FailoverPolicy())
	}
	empty := config.Default()
	if empty.FailoverPolicy() != "round-robin" {
		t.Fatalf("default policy %q", empty.FailoverPolicy())
	}
}

func TestValidateRejectsUnknownFailoverPolicy(t *testing.T) {
	cfg := config.Default()
	cfg.Failover.Policy = "least-used"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "failover.policy") {
		t.Fatalf("unknown policy: %v", err)
	}
	cfg.Failover.Policy = "sticky"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyEnvFailoverPolicy(t *testing.T) {
	cfg := config.Default()
	t.Setenv("PEAPROXY_FAILOVER_POLICY", "sticky")
	config.ApplyEnv(&cfg)
	if cfg.FailoverPolicy() != "sticky" {
		t.Fatalf("%q", cfg.FailoverPolicy())
	}
}

func TestValidateRejectsEmptyAdapterAndBadTier(t *testing.T) {
	cfg := config.Default()
	cfg.Providers[0].Adapter = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "adapter") {
		t.Fatalf("empty adapter: %v", err)
	}
	cfg = config.Default()
	cfg.Providers[0].Tier = "enterprise"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "tier") {
		t.Fatalf("bad tier: %v", err)
	}
}

func TestValidateCatalogOverlays(t *testing.T) {
	cfg := config.Default()
	cfg.Catalog.Pin = []string{"llama3.2", "llama3.2"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "pin") {
		t.Fatalf("duplicate pin: %v", err)
	}
	cfg = config.Default()
	cfg.Catalog.Pin = []string{"  "}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "pin") {
		t.Fatalf("empty pin: %v", err)
	}
	cfg = config.Default()
	cfg.Catalog.Rename = map[string]string{"": "Nope"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "rename") {
		t.Fatalf("empty rename key: %v", err)
	}
	cfg = config.Default()
	cfg.Catalog.Rename = map[string]string{"llama3.2": "   "}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "rename") {
		t.Fatalf("empty rename value: %v", err)
	}
	cfg = config.Default()
	cfg.Hide.Models = []string{"ok", ""}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "hide.models") {
		t.Fatalf("empty hide model: %v", err)
	}
	cfg = config.Default()
	cfg.Catalog.Pin = []string{"llama3.2"}
	cfg.Catalog.Rename = map[string]string{"llama3.2": "Llama 3.2 local"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateKnownAdapters(t *testing.T) {
	cfg := config.Default()
	if err := cfg.ValidateKnownAdapters([]string{"ollama", "openai"}); err != nil {
		t.Fatal(err)
	}
	cfg.Providers[0].Adapter = "not-a-real-adapter"
	err := cfg.ValidateKnownAdapters([]string{"ollama", "openai"})
	if err == nil || !strings.Contains(err.Error(), "unknown adapter") {
		t.Fatalf("got %v", err)
	}
}

func TestNeedsOnboarding(t *testing.T) {
	if !config.Default().NeedsOnboarding() {
		t.Fatal("first-run default should show onboarding")
	}
	empty := config.Default()
	empty.Providers = nil
	if !empty.NeedsOnboarding() {
		t.Fatal("no accounts should show onboarding")
	}
	keyed := config.Default()
	keyed.Providers[0].APIKeyEnv = "OLLAMA_API_KEY"
	if keyed.NeedsOnboarding() {
		t.Fatal("configured local account is past onboarding")
	}
	two := config.Default()
	two.Providers = append(two.Providers, config.Provider{ID: "openai-key", Adapter: "openai", Tier: "paid"})
	if two.NeedsOnboarding() {
		t.Fatal("second account ends onboarding")
	}
}
