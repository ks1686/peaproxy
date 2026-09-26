package clients

import (
	"strings"
	"testing"
)

func TestListIncludesHarnessesFromPlan(t *testing.T) {
	got := map[string]bool{}
	for _, n := range List() {
		got[n] = true
	}
	for _, want := range []string{"cursor", "claude-code", "opencode", "pi", "codex", "continue"} {
		if !got[want] {
			t.Fatalf("missing preset %s in %v", want, List())
		}
	}
}

func TestGetPiDocumentsCloakWarning(t *testing.T) {
	p, ok := Get("pi")
	if !ok {
		t.Fatal("missing pi")
	}
	if p.Notes == "" {
		t.Fatal("pi notes must mention cloak defaults")
	}
}

func TestOpenCodeAndClaudeCodeUseDifferentBaseURLs(t *testing.T) {
	cc, ok := Get("claude-code")
	if !ok {
		t.Fatal("missing claude-code")
	}
	oc, ok := Get("opencode")
	if !ok {
		t.Fatal("missing opencode")
	}
	if !strings.Contains(cc.Snippet, "ANTHROPIC_BASE_URL=http://127.0.0.1:8317\n") {
		t.Fatalf("claude-code must omit /v1: %s", cc.Snippet)
	}
	if strings.Contains(cc.Snippet, "8317/v1") {
		t.Fatal("claude-code snippet should not include /v1 on ANTHROPIC_BASE_URL")
	}
	if !strings.Contains(oc.Snippet, "http://127.0.0.1:8317/v1") {
		t.Fatalf("opencode must include /v1: %s", oc.Snippet)
	}
}
