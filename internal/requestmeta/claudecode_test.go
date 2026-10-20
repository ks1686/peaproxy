package requestmeta

import (
	"net/http"
	"strings"
	"testing"
)

func claudeCodeHeader(ua, app string) http.Header {
	h := http.Header{}
	h.Set("User-Agent", ua)
	h.Set("X-App", app)
	h.Set("X-Stainless-Package-Version", "0.120.0")
	h.Set("X-Stainless-Runtime", "node")
	h.Set("X-Stainless-Runtime-Version", "v26.3.0")
	h.Set("X-Stainless-Lang", "js")
	h.Set("X-Stainless-Os", "MacOS")
	h.Set("X-Stainless-Arch", "arm64")
	h.Set("X-Stainless-Timeout", "600")
	h.Set("X-Stainless-Retry-Count", "0")
	h.Set("X-Claude-Code-Session-Id", "694d6b7f-cc15-49a2-8118-e5a45516f7d8")
	h.Set("Authorization", "Bearer client-token")
	h.Set("Anthropic-Beta", "claude-code-20250219")
	return h
}

func TestClaudeCodeHeadersRecognizesNativeEntrypoints(t *testing.T) {
	for _, ua := range []string{
		"claude-cli/2.1.300 (external, cli)",
		"claude-cli/2.1.300 (external, sdk-cli)",
		"claude-cli/2.1.300 (external, sdk-ts, agent-sdk/0.3.1)",
		"claude-cli/2.1.300 (external, claude-vscode)",
	} {
		got := ClaudeCodeHeaders(claudeCodeHeader(ua, "cli"))
		if len(got) != len(claudeCodeIdentityHeaders) || got[0] != (Header{"User-Agent", ua}) || got[1] != (Header{"X-Stainless-Package-Version", "0.120.0"}) {
			t.Fatalf("%q: got %#v", ua, got)
		}
	}
}

func TestClaudeCodeHeadersNeverCarriesCredentialsBetasOrRetryCount(t *testing.T) {
	for _, h := range ClaudeCodeHeaders(claudeCodeHeader("claude-cli/2.1.300 (external, cli)", "cli")) {
		if h.Name == "Authorization" || h.Name == "Anthropic-Beta" || h.Name == "X-Stainless-Retry-Count" {
			t.Fatalf("forwarded %s", h.Name)
		}
	}
}

func TestClaudeCodeHeadersRejectsPartialIdentity(t *testing.T) {
	// Forwarding only some identity headers would mix the client's values with
	// the adapter's fixed 2.1.280 ones.
	for _, name := range claudeCodeIdentityHeaders[1:] {
		h := claudeCodeHeader("claude-cli/2.1.300 (external, cli)", "cli")
		h.Del(name)
		if got := ClaudeCodeHeaders(h); got != nil {
			t.Fatalf("without %s: got %#v, want nil", name, got)
		}
	}
}

func TestClaudeCodeHeadersRejectsOtherClients(t *testing.T) {
	cases := map[string]http.Header{
		"no x-app":             claudeCodeHeader("claude-cli/2.1.300 (external, cli)", ""),
		"unchecked entrypoint": claudeCodeHeader("claude-cli/2.1.300 (external, sdk-py)", "cli"),
		"bare prefix":          claudeCodeHeader("claude-cli/2.1.300", "cli"),
		"trailing text":        claudeCodeHeader("claude-cli/2.1.300 (external, cli) pi/1.0", "cli"),
		"other client":         claudeCodeHeader("opencode/1.0", "cli"),
		"oversized user agent": claudeCodeHeader("claude-cli/2.1."+strings.Repeat("9", 120)+" (external, cli)", "cli"),
	}
	for name, h := range cases {
		if got := ClaudeCodeHeaders(h); got != nil {
			t.Fatalf("%s: got %#v, want nil", name, got)
		}
	}
}

func TestClaudeCodeHeadersRejectsUnsafeValues(t *testing.T) {
	for name, v := range map[string]string{"X-Stainless-Os": "Mac\x7fOS", "X-Stainless-Arch": strings.Repeat("a", 129)} {
		h := claudeCodeHeader("claude-cli/2.1.300 (external, cli)", "cli")
		h.Set(name, v)
		if got := ClaudeCodeHeaders(h); got != nil {
			t.Fatalf("unsafe %s: got %#v, want nil", name, got)
		}
	}
}
