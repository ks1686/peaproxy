package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootHelpListsPlanCommands(t *testing.T) {
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"--help"}, out)
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, cmd := range []string{"serve", "auth", "accounts", "models", "status", "config", "clients"} {
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

func TestVersionFlag(t *testing.T) {
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"--version"}, out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "peaproxy") {
		t.Fatalf("%s", out)
	}
}

func TestClientsListIncludesClineAndContinue(t *testing.T) {
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"clients", "list"}, out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, name := range []string{"cline", "continue", "pi", "cursor"} {
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
	if !strings.Contains(s, "ANTHROPIC_BASE_URL=http://127.0.0.1:8317\n") {
		t.Fatalf("%s", s)
	}
	if !strings.Contains(s, "OPENAI_BASE_URL=http://127.0.0.1:8317/v1") {
		t.Fatalf("%s", s)
	}
}

func TestAuthLoginPrintsOfficialKeyDocs(t *testing.T) {
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"auth", "login", "--provider", "anthropic"}, out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "console.anthropic.com") {
		t.Fatalf("%s", out)
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
