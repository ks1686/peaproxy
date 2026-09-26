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
	for _, want := range []string{"cursor", "claude-code", "opencode", "pi", "codex", "continue", "cline", "amp", "droid"} {
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
	if p.Cloak != "off" {
		t.Fatalf("pi cloak defaults must be off, got %q", p.Cloak)
	}
	if !strings.Contains(strings.ToLower(p.Notes), "cloak") {
		t.Fatal("pi notes must mention cloak defaults")
	}
	if !strings.Contains(p.Notes, "#6120") {
		t.Fatal("pi notes should cite CLIProxyAPI cloak issue")
	}
	if !strings.Contains(p.Snippet, "ANTHROPIC_BASE_URL=http://127.0.0.1:8317\n") {
		t.Fatalf("pi anthropic wire must omit /v1: %s", p.Snippet)
	}
	if !strings.Contains(p.Snippet, "OPENAI_BASE_URL=http://127.0.0.1:8317/v1") {
		t.Fatalf("pi openai wire must include /v1: %s", p.Snippet)
	}
	if strings.Contains(strings.ToLower(p.Snippet), "clear_thinking") {
		t.Fatal("pi snippet must not inject clear_thinking")
	}
}

func TestContinueAndClinePresets(t *testing.T) {
	c, ok := Get("continue")
	if !ok {
		t.Fatal("missing continue")
	}
	if !strings.Contains(c.Snippet, "apiBase:") && !strings.Contains(c.Snippet, `"apiBase"`) {
		t.Fatalf("continue must set apiBase: %s", c.Snippet)
	}
	if !strings.Contains(c.Snippet, "schema: v1") {
		t.Fatalf("continue 1.x yaml: %s", c.Snippet)
	}
	cl, ok := Get("cline")
	if !ok {
		t.Fatal("missing cline")
	}
	if !strings.Contains(cl.Snippet, "OpenAI Compatible") {
		t.Fatalf("cline: %s", cl.Snippet)
	}
	if !strings.Contains(cl.Notes, "stream_options") {
		t.Fatal("cline notes must mention stream_options passthrough")
	}
	if !strings.Contains(cl.Notes, "not the Cline cloud") {
		t.Fatalf("cline must warn against Cline-as-service: %s", cl.Notes)
	}
}

func TestAmpPresetIsCustomURLNotManagement(t *testing.T) {
	p, ok := Get("amp")
	if !ok {
		t.Fatal("missing amp")
	}
	if p.Cloak != "off" {
		t.Fatalf("amp cloak %q", p.Cloak)
	}
	if !strings.Contains(p.Snippet, "http://127.0.0.1:8317/v1") {
		t.Fatalf("amp base: %s", p.Snippet)
	}
	if !strings.Contains(p.Snippet, "chat-completions") {
		t.Fatalf("amp format: %s", p.Snippet)
	}
	if strings.Contains(p.Snippet, `"amp.url"`) || strings.Contains(p.Snippet, "amp.url:") {
		t.Fatal("snippet must not tell users to set amp.url to PeaProxy")
	}
	if !strings.Contains(p.Notes, "amp.url") {
		t.Fatal("notes must warn against amp.url")
	}
	if !strings.Contains(p.Notes, "WebSocket") {
		t.Fatal("notes must mention no Amp WebSocket")
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

func TestEveryPresetHasVerifyCommand(t *testing.T) {
	for _, name := range List() {
		p, _ := Get(name)
		want := "peaproxy clients verify " + name
		if !strings.Contains(p.Verify, want) {
			t.Fatalf("%s missing verify command, got %q", name, p.Verify)
		}
	}
}

func TestEveryPresetDocumentsCloak(t *testing.T) {
	for _, name := range List() {
		p, _ := Get(name)
		if p.Cloak != "off" && p.Cloak != "opt-in" {
			t.Fatalf("%s cloak %q", name, p.Cloak)
		}
		if name != "claude-code" && p.Cloak != "off" {
			t.Fatalf("%s must default cloak off, got %q", name, p.Cloak)
		}
		if name == "claude-code" && p.Cloak != "opt-in" {
			t.Fatalf("claude-code cloak should be opt-in, got %q", p.Cloak)
		}
	}
}

func TestCodexPresetDocumentsResponses(t *testing.T) {
	p, ok := Get("codex")
	if !ok {
		t.Fatal("missing codex")
	}
	if !strings.Contains(p.Snippet, "wire_api = \"responses\"") {
		t.Fatalf("codex snippet must set wire_api responses: %s", p.Snippet)
	}
	if strings.Contains(p.Notes, "TODO") {
		t.Fatalf("codex notes still TODO: %s", p.Notes)
	}
	if !strings.Contains(p.Notes, "stream_options") {
		t.Fatal("codex notes should mention stream_options strip on OAuth")
	}
}

func TestDroidPresetIsBYOKClientNotUpstream(t *testing.T) {
	p, ok := Get("droid")
	if !ok {
		t.Fatal("missing droid")
	}
	if p.Cloak != "off" {
		t.Fatalf("droid cloak %q", p.Cloak)
	}
	if !strings.Contains(p.Snippet, "generic-chat-completion-api") {
		t.Fatalf("droid must use Factory BYOK chat-completions: %s", p.Snippet)
	}
	if !strings.Contains(p.Snippet, "http://127.0.0.1:8317/v1") {
		t.Fatalf("droid base: %s", p.Snippet)
	}
	if !strings.Contains(p.Notes, "not a chat-model upstream") {
		t.Fatalf("droid notes: %s", p.Notes)
	}
}
