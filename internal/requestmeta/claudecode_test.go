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
		if len(got) != 2 || got[0] != (Header{"User-Agent", ua}) || got[1] != (Header{"X-Stainless-Package-Version", "0.120.0"}) {
			t.Fatalf("%q: got %#v", ua, got)
		}
	}
}

func TestClaudeCodeHeadersNeverCarriesCredentialsOrBetas(t *testing.T) {
	for _, h := range ClaudeCodeHeaders(claudeCodeHeader("claude-cli/2.1.300 (external, cli)", "cli")) {
		if h.Name == "Authorization" || h.Name == "Anthropic-Beta" {
			t.Fatalf("forwarded %s", h.Name)
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

func TestClaudeCodeHeadersDropsUnsafeValues(t *testing.T) {
	h := claudeCodeHeader("claude-cli/2.1.300 (external, cli)", "cli")
	h.Set("X-Stainless-Os", "Mac\x7fOS")
	h.Set("X-Stainless-Arch", strings.Repeat("a", 129))
	for _, got := range ClaudeCodeHeaders(h) {
		if got.Name == "X-Stainless-Os" || got.Name == "X-Stainless-Arch" {
			t.Fatalf("unsafe %s forwarded: %q", got.Name, got.Value)
		}
	}
}
