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
// forwards them. Every identity header must be present, in printable ASCII
// and at most 128 bytes: forwarding only some of them would pair the client's
// values with the adapter's fixed 2.1.280 ones, a mix no real build sends, and
// a bounded value cannot carry header syntax.
func ClaudeCodeHeaders(h http.Header) []Header {
	if h.Get("X-App") != "cli" || !claudeCodeNativeUserAgent.MatchString(h.Get("User-Agent")) {
		return nil
	}
	out := make([]Header, 0, len(claudeCodeIdentityHeaders))
	for _, name := range claudeCodeIdentityHeaders {
		v := strings.TrimSpace(h.Get(name))
		if !safeHeaderValue(v) {
			return nil
		}
		out = append(out, Header{Name: name, Value: v})
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
