package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/version"
)

func TestRootHelpListsPlanCommands(t *testing.T) {
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"--help"}, out)
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, cmd := range []string{"serve", "auth", "accounts", "models", "status", "config", "clients", "catalog", "requests", "health"} {
		if !strings.Contains(got, cmd) {
			t.Fatalf("root help missing %q:\n%s", cmd, got)
		}
	}
}

func TestClientsShowCursor(t *testing.T) {
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"clients", "show", "cursor"}, out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "127.0.0.1:8317") {
		t.Fatalf("cursor preset:\n%s", out)
	}
}

func TestAuthLoginWithoutProviderErrors(t *testing.T) {
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"auth", "login"}, out)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "peaproxy auth login --provider") {
		t.Fatalf("error should include example invocation, got %v", err)
	}
}

func TestStatusUsesDefaultBind(t *testing.T) {
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"status"}, out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "127.0.0.1:8317") {
		t.Fatalf("%s", out)
	}
}

func TestConfigValidateOK(t *testing.T) {
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"config", "validate"}, out); err != nil {
		t.Fatal(err)
	}
}

func TestConfigValidateReportsOverlaysRequestLogAndSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
requestLog: true
catalog:
  pin:
    - llama3.2
  rename:
    llama3.2: Llama local
providers:
  - id: ollama-local
    adapter: ollama
    tier: local
    baseURL: http://127.0.0.1:11434/v1
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"config", "validate", "--config", path}, out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"ok", "requestLog: true", "catalog.pin: 1", "catalog.rename: 1", "failover.policy: round-robin", "secrets: file"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestConfigValidateRejectsUnknownAdapter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: weird
    adapter: not-a-real-adapter
    tier: paid
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"config", "validate", "--config", path}, out)
	if err == nil || !strings.Contains(err.Error(), "unknown adapter") {
		t.Fatalf("got %v\n%s", err, out)
	}
}

func TestVersionFlag(t *testing.T) {
	orig := version.Version
	version.Version = "v1.6.0"
	t.Cleanup(func() { version.Version = orig })
	for _, args := range [][]string{{"--version"}, {"-v"}} {
		out := &bytes.Buffer{}
		if err := ExecuteWithArgs(args, out); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); got != "peaproxy v1.6.0\n" {
			t.Fatalf("%v: got %q", args, got)
		}
	}
}

func TestClientsListIncludesClineAndContinue(t *testing.T) {
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"clients", "list"}, out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, name := range []string{"cline", "continue", "pi", "cursor", "amp", "droid"} {
		if !strings.Contains(got, name) {
			t.Fatalf("missing %s in %s", name, got)
		}
	}
}

func TestClientsShowPiBothWires(t *testing.T) {
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"clients", "show", "pi"}, out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, `"anthropic": { "baseUrl": "http://127.0.0.1:8317"`) {
		t.Fatalf("%s", s)
	}
	if !strings.Contains(s, `"openai": { "baseUrl": "http://127.0.0.1:8317/v1"`) {
		t.Fatalf("%s", s)
	}
	if strings.Contains(s, "_BASE_URL=") {
		t.Fatalf("pi does not read base URLs from the environment: %s", s)
	}
}

func TestAuthLoginPrintsOfficialKeyDocs(t *testing.T) {
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"auth", "login", "--provider", "anthropic", "--print-url"}, out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "console.anthropic.com") {
		t.Fatalf("%s", got)
	}
	if !strings.Contains(got, "not liable") || !strings.Contains(got, "claude.ai/oauth/authorize") {
		t.Fatalf("expected liability warning and login URL:\n%s", got)
	}
}

func TestAuthLoginGeminiPrintURL(t *testing.T) {
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"auth", "login", "--provider", "gemini", "--print-url", "--no-browser"}, out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "accounts.google.com") {
		t.Fatalf("%s", got)
	}
	if !strings.Contains(got, "not liable") || !strings.Contains(got, "aistudio.google.com") {
		t.Fatalf("%s", got)
	}
}

func TestAuthLoginQwenNotYet(t *testing.T) {
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"auth", "login", "--provider", "qwen", "--print-url"}, out)
	if err == nil || !strings.Contains(err.Error(), "not yet") {
		t.Fatalf("want not yet, got %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "not liable") {
		t.Fatalf("warning should print before not-yet: %s", out)
	}
}

func TestAuthLoginFactoryNotYet(t *testing.T) {
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"auth", "login", "--provider", "factory", "--print-url"}, out)
	if err == nil || !strings.Contains(err.Error(), "not yet") {
		t.Fatalf("want not yet, got %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "not liable") {
		t.Fatalf("warning should print before not-yet: %s", out)
	}
}

func TestAuthLoginOpenCodeGoIsAPIKey(t *testing.T) {
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"auth", "login", "--provider", "opencode-go", "--print-url"}, out)
	if err == nil || !strings.Contains(err.Error(), "opencode.ai/auth") {
		t.Fatalf("want API-key instructions, got %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "not liable") {
		t.Fatalf("warning should print first: %s", out)
	}
}

func TestAuthLoginDroidAliasIsFactoryStub(t *testing.T) {
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"auth", "login", "--provider", "droid", "--print-url"}, out)
	if err == nil || !strings.Contains(err.Error(), "not yet") {
		t.Fatalf("droid should map to factory stub, got %v\n%s", err, out)
	}
}

func TestServeRefusesBindAllWithoutAllowLAN(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/peaproxy.yaml"
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"serve", "--config", path, "--bind", "0.0.0.0"}, out)
	if err == nil {
		t.Fatal("expected bind-all without --allow-lan to fail")
	}
	if !strings.Contains(err.Error(), "allow-lan") {
		t.Fatalf("error %v", err)
	}
}

func TestConfigInitWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/peaproxy.yaml"
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"config", "init", "--config", path}, out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wrote") {
		t.Fatalf("%s", out)
	}
}

func TestAccountsListOmitsSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.Provider{
		ID:      "anthropic-oauth",
		Adapter: "anthropic_oauth",
		Tier:    "paid",
		OAuth: &config.OAuthToken{
			AccessToken:  "secret-access-token",
			RefreshToken: "secret-refresh-token",
			Email:        "a@b.c",
		},
	})
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"accounts", "list", "--config", path}, out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "secret-access-token") || strings.Contains(got, "secret-refresh-token") {
		t.Fatalf("token leaked:\n%s", got)
	}
	if !strings.Contains(got, "anthropic-oauth") || !strings.Contains(got, "auth=oauth") {
		t.Fatalf("%s", got)
	}
}

func TestConfigShowReportsSecretBackend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	if err := os.WriteFile(path, []byte("schemaVersion: 1\nbind: 127.0.0.1\nport: 8317\nproviders: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"config", "show", "--config", path}, out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "secrets: file") {
		t.Fatalf("%s", got)
	}
}
