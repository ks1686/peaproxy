// Package clients holds copy-ready harness presets. Verify is a spike TODO.
package clients

import (
	"fmt"
	"sort"
	"strings"
)

// Preset is a coding-tool config snippet pointing at PeaProxy.
type Preset struct {
	Name       string
	BaseURL    string
	AuthHeader string
	Notes      string
	Snippet    string
	VerifyTODO string
}

var presets = map[string]Preset{
	"cursor": {
		Name:       "cursor",
		BaseURL:    "http://127.0.0.1:8317/v1",
		AuthHeader: "Authorization: Bearer peaproxy (optional on loopback)",
		Notes:      "OpenAI-compatible. Set OpenAI Base URL in Cursor models.",
		Snippet: `OpenAI Base URL: http://127.0.0.1:8317/v1
API Key: peaproxy
`,
		VerifyTODO: "peaproxy clients verify cursor — GET /v1/models through Cursor's base URL",
	},
	"claude-code": {
		Name:       "claude-code",
		BaseURL:    "http://127.0.0.1:8317",
		AuthHeader: "x-api-key: peaproxy (optional on loopback)",
		Notes:      "Anthropic Messages API. /v1/messages is stubbed until the spike.",
		Snippet: `ANTHROPIC_BASE_URL=http://127.0.0.1:8317
ANTHROPIC_API_KEY=peaproxy
`,
		VerifyTODO: "POST /v1/messages smoke once Claude adapter is live",
	},
	"opencode": {
		Name:       "opencode",
		BaseURL:    "http://127.0.0.1:8317/v1",
		AuthHeader: "Authorization: Bearer peaproxy",
		Notes:      "OpenAI-compat provider block in opencode.json.",
		Snippet: `{
  "provider": {
    "peaproxy": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "http://127.0.0.1:8317/v1" }
    }
  }
}
`,
		VerifyTODO: "opencode model list against peaproxy",
	},
	"pi": {
		Name:       "pi",
		BaseURL:    "http://127.0.0.1:8317",
		AuthHeader: "depends on anthropic-messages vs OpenAI path",
		Notes:      "Do not apply Claude-Code cloak defaults to Pi. See docs/HARNESS.md.",
		Snippet: `# Anthropic-messages path
ANTHROPIC_BASE_URL=http://127.0.0.1:8317
# OpenAI path
OPENAI_BASE_URL=http://127.0.0.1:8317/v1
`,
		VerifyTODO: "golden test: Pi without thinking/cloak injection",
	},
	"codex": {
		Name:       "codex",
		BaseURL:    "http://127.0.0.1:8317/v1",
		AuthHeader: "Authorization: Bearer peaproxy",
		Notes:      "Responses API vs Chat Completions — wire notes in docs/HARNESS.md. Not implemented.",
		Snippet: `OPENAI_BASE_URL=http://127.0.0.1:8317/v1
OPENAI_API_KEY=peaproxy
`,
		VerifyTODO: "Codex Responses endpoint once adapter exists",
	},
	"continue": {
		Name:       "continue",
		BaseURL:    "http://127.0.0.1:8317/v1",
		AuthHeader: "Authorization: Bearer peaproxy",
		Notes:      "Generic OpenAI-compat template.",
		Snippet: `{
  "models": [{
    "provider": "openai",
    "apiBase": "http://127.0.0.1:8317/v1",
    "apiKey": "peaproxy"
  }]
}
`,
		VerifyTODO: "continue model dropdown lists live catalog",
	},
}

// List returns preset names in stable order.
func List() []string {
	names := make([]string, 0, len(presets))
	for k := range presets {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Get returns a named preset.
func Get(name string) (Preset, bool) {
	p, ok := presets[strings.ToLower(name)]
	return p, ok
}

// Format prints a preset for humans and agents.
func Format(p Preset) string {
	return fmt.Sprintf("name: %s\nbase_url: %s\nauth: %s\nnotes: %s\n\n# snippet\n%s\n# verify: %s\n",
		p.Name, p.BaseURL, p.AuthHeader, p.Notes, p.Snippet, p.VerifyTODO)
}
