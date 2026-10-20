package requestmeta

import (
	"net/http"
	"regexp"
	"strings"
)

// Header is one client header value carried to an adapter.
type Header struct {
	Name, Value string
}

// claudeCodeNativeUserAgent matches the User-Agent of the Claude Code builds
// whose own wire shape a subscription token accepts: the terminal CLI, the
// Agent SDK CLI and TypeScript SDK, and the VS Code extension. Other
// first-party-looking entrypoints (sdk-py, desktop, remote) are not listed
// because their request shape has not been checked against this path.
var claudeCodeNativeUserAgent = regexp.MustCompile(`^claude-cli/[0-9]+\.[0-9]+\.[0-9]+ \(external, (cli|sdk-cli|sdk-ts|claude-vscode)(, agent-sdk/[0-9]+\.[0-9]+\.[0-9]+)?\)$`)

// claudeCodeIdentityHeaders are the headers a native Claude Code client sends
// about itself. Credentials and anthropic-beta are not among them: the adapter
// supplies its own token, and betas travel in Request.AnthropicBeta.
// X-Stainless-Retry-Count is left out: PeaProxy's own failover resends the
// request, and the client's count would not reflect that.
var claudeCodeIdentityHeaders = []string{
	"User-Agent",
	"X-Stainless-Package-Version",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"X-Stainless-Lang",
	"X-Stainless-Os",
	"X-Stainless-Arch",
	"X-Stainless-Timeout",
	"X-Claude-Code-Session-Id",
}

const maxClaudeCodeHeaderLen = 128

// ClaudeCodeHeaders returns the identity headers of a native Claude Code
// client, or nil when h does not come from one. Headers alone only say the
// client claims to be Claude Code; the adapter also checks the body before it
// forwards them. Values outside printable ASCII or longer than 128 bytes are
// dropped so a forwarded value cannot carry header syntax.
func ClaudeCodeHeaders(h http.Header) []Header {
	ua := h.Get("User-Agent")
	// An unsafe User-Agent would be dropped below and leave the other headers
	// paired with the fixed one, a mix no real build sends.
	if h.Get("X-App") != "cli" || !safeHeaderValue(ua) || !claudeCodeNativeUserAgent.MatchString(ua) {
		return nil
	}
	var out []Header
	for _, name := range claudeCodeIdentityHeaders {
		if v := strings.TrimSpace(h.Get(name)); safeHeaderValue(v) {
			out = append(out, Header{Name: name, Value: v})
		}
	}
	return out
}

func safeHeaderValue(v string) bool {
	if v == "" || len(v) > maxClaudeCodeHeaderLen {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return false
		}
	}
	return true
}
