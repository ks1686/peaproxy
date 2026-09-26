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
