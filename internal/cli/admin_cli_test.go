package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/config"
)

func writeEmptyCfg(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte("schemaVersion: 1\nbind: 127.0.0.1\nport: 8317\nproviders: []\n")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadSaved(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCatalogPinRenameHidePersist(t *testing.T) {
	path := writeEmptyCfg(t)
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"catalog", "pin", "llama3.2", "--config", path}, out); err != nil {
		t.Fatalf("pin: %v\n%s", err, out)
	}
	cfg := loadSaved(t, path)
	if len(cfg.Catalog.Pin) != 1 || cfg.Catalog.Pin[0] != "llama3.2" {
		t.Fatalf("pin not saved: %#v", cfg.Catalog)
	}

	out.Reset()
	if err := ExecuteWithArgs([]string{"catalog", "rename", "llama3.2", "Llama local", "--config", path}, out); err != nil {
		t.Fatalf("rename: %v\n%s", err, out)
	}
	cfg = loadSaved(t, path)
	if cfg.Catalog.Rename["llama3.2"] != "Llama local" {
		t.Fatalf("rename not saved: %#v", cfg.Catalog.Rename)
	}

	out.Reset()
	if err := ExecuteWithArgs([]string{"catalog", "hide", "llama3.2", "--config", path}, out); err != nil {
		t.Fatalf("hide: %v\n%s", err, out)
	}
	cfg = loadSaved(t, path)
	if len(cfg.Hide.Models) != 1 || cfg.Hide.Models[0] != "llama3.2" {
		t.Fatalf("hide not saved: %#v", cfg.Hide)
	}

	out.Reset()
	if err := ExecuteWithArgs([]string{"catalog", "pin", "llama3.2", "--off", "--config", path}, out); err != nil {
		t.Fatalf("unpin: %v\n%s", err, out)
	}
	out.Reset()
	if err := ExecuteWithArgs([]string{"catalog", "hide", "llama3.2", "--off", "--config", path}, out); err != nil {
		t.Fatalf("unhide: %v\n%s", err, out)
	}
	out.Reset()
	if err := ExecuteWithArgs([]string{"catalog", "rename", "llama3.2", "--clear", "--config", path}, out); err != nil {
		t.Fatalf("clear rename: %v\n%s", err, out)
	}
	cfg = loadSaved(t, path)
	if len(cfg.Catalog.Pin) != 0 || len(cfg.Catalog.Rename) != 0 || len(cfg.Hide.Models) != 0 {
		t.Fatalf("overlays should be cleared: pin=%v rename=%v hide=%v", cfg.Catalog.Pin, cfg.Catalog.Rename, cfg.Hide.Models)
	}
}

func TestCatalogHideProviderKind(t *testing.T) {
	path := writeEmptyCfg(t)
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"catalog", "hide", "jan", "--kind", "provider", "--config", path}, out); err != nil {
		t.Fatalf("hide provider: %v\n%s", err, out)
	}
	cfg := loadSaved(t, path)
	if len(cfg.Hide.Providers) != 1 || cfg.Hide.Providers[0] != "jan" {
		t.Fatalf("provider hide: %#v", cfg.Hide)
	}
}

func TestAccountsAddFromJanGPT4AllSambaNovaPresets(t *testing.T) {
	path := writeEmptyCfg(t)
	cases := []struct {
		preset  string
		adapter string
		base    string
		envKey  string
	}{
		{"jan-local", "jan", "http://127.0.0.1:1337/v1", ""},
		{"gpt4all-local", "gpt4all", "http://127.0.0.1:4891/v1", ""},
		{"sambanova-key", "sambanova", "https://api.sambanova.ai/v1", "SAMBANOVA_API_KEY"},
	}
	for _, tc := range cases {
		out := &bytes.Buffer{}
		if err := ExecuteWithArgs([]string{"accounts", "add", tc.preset, "--config", path}, out); err != nil {
			t.Fatalf("add %s: %v\n%s", tc.preset, err, out)
		}
	}
	cfg := loadSaved(t, path)
	got := map[string]config.Provider{}
	for _, p := range cfg.Providers {
		got[p.ID] = p
	}
	for _, tc := range cases {
		p, ok := got[tc.preset]
		if !ok {
			t.Fatalf("missing account %s in %#v", tc.preset, cfg.Providers)
		}
		if p.Adapter != tc.adapter || p.BaseURL != tc.base {
			t.Fatalf("%s: adapter=%s base=%s", tc.preset, p.Adapter, p.BaseURL)
		}
		if p.APIKeyEnv != tc.envKey {
			t.Fatalf("%s apiKeyEnv=%s want %s", tc.preset, p.APIKeyEnv, tc.envKey)
		}
	}
}

func TestAccountsAddWorkersAIFillsAccountIDFromEnv(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "cf_acct_cli")
	path := writeEmptyCfg(t)
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"accounts", "add", "workers-ai", "--config", path}, out); err != nil {
		t.Fatalf("add workers-ai: %v\n%s", err, out)
	}
	cfg := loadSaved(t, path)
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers: %#v", cfg.Providers)
	}
	p := cfg.Providers[0]
	if p.Adapter != "workers_ai" || p.APIKeyEnv != "CLOUDFLARE_API_TOKEN" {
		t.Fatalf("%#v", p)
	}
	if !strings.Contains(p.BaseURL, "cf_acct_cli") {
		t.Fatalf("expected filled account id in %s", p.BaseURL)
	}
	if strings.Contains(p.BaseURL, "YOUR_ACCOUNT_ID") {
		t.Fatal("placeholder should be gone")
	}
}

func TestAccountsAddWorkersAIAccountIDFlag(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	path := writeEmptyCfg(t)
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"accounts", "add", "workers-ai", "--account-id", "flag_acct_99", "--config", path}, out); err != nil {
		t.Fatalf("add workers-ai flag: %v\n%s", err, out)
	}
	cfg := loadSaved(t, path)
	if !strings.Contains(cfg.Providers[0].BaseURL, "flag_acct_99") {
		t.Fatalf("flag account id not applied: %s", cfg.Providers[0].BaseURL)
	}
	if strings.Contains(cfg.Providers[0].BaseURL, "YOUR_ACCOUNT_ID") {
		t.Fatal("placeholder should be gone")
	}
}

func TestAccountsAddWorkersAIRequiresAccountID(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	path := writeEmptyCfg(t)
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"accounts", "add", "workers-ai", "--config", path}, out)
	if err == nil || !strings.Contains(err.Error(), "account id") {
		t.Fatalf("want account-id error, got %v\n%s", err, out)
	}
}

func TestHealthMatchesAdminHealthFields(t *testing.T) {
	path := writeEmptyCfg(t)
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"health", "--config", path}, out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"status: ok",
		"bind: 127.0.0.1",
		"port: 8317",
		"requestLog: false",
		"models:",
		"adapters:",
		"adapterHealth:",
		"cooldowns:",
		"failoverPolicy: round-robin",
		"cooldownTtlMs: 30000",
		"allowNonLoopback: false",
		"lan: false",
		"adminTokenRequired: false",
		"oauth:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRequestsTailRequiresRequestLog(t *testing.T) {
	path := writeEmptyCfg(t)
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"requests", "tail", "--config", path}, out)
	if err == nil || !strings.Contains(err.Error(), "requestLog") {
		t.Fatalf("want requestLog error, got %v\n%s", err, out)
	}
}

func TestRequestsTailPrintsRedactedLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte("schemaVersion: 1\nbind: 127.0.0.1\nport: 8317\nrequestLog: true\nproviders: []\n")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "requests.log")
	line := `{"accountId":"jan-local","model":"llama3.2","protocol":"openai","status":200,"preview":"hi"}` + "\n"
	if err := os.WriteFile(logPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"requests", "tail", "--config", path}, out); err != nil {
		t.Fatalf("tail: %v\n%s", err, out)
	}
	got := out.String()
	if !strings.Contains(got, "jan-local") || !strings.Contains(got, "llama3.2") {
		t.Fatalf("tail missing event:\n%s", got)
	}
	if strings.Contains(got, "sk-") {
		t.Fatalf("secret leaked:\n%s", got)
	}
}
