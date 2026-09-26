// Package clients holds copy-ready harness presets.
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
		AuthHeader: "Authorization: Bearer peaproxy",
		Notes:      "OpenAI-compatible. Settings → Models → Override OpenAI Base URL.",
		Snippet: `OpenAI Base URL: http://127.0.0.1:8317/v1
API Key: peaproxy
`,
		VerifyTODO: "peaproxy clients verify cursor",
	},
	"claude-code": {
		Name:       "claude-code",
		BaseURL:    "http://127.0.0.1:8317",
		AuthHeader: "x-api-key: peaproxy",
		Notes:      "Anthropic Messages. Claude Code appends /v1/messages — do NOT put /v1 on ANTHROPIC_BASE_URL (differs from OpenCode).",
		Snippet: `# Claude Code — base URL WITHOUT /v1
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_API_KEY=peaproxy
`,
		VerifyTODO: "peaproxy clients verify claude-code",
	},
	"opencode": {
		Name:       "opencode",
		BaseURL:    "http://127.0.0.1:8317/v1",
		AuthHeader: "Authorization: Bearer peaproxy",
		Notes:      "OpenCode Anthropic provider wants baseURL INCLUDING /v1. Do not reuse the Claude Code env as-is.",
		Snippet: `{
  "provider": {
    "peaproxy-openai": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "http://127.0.0.1:8317/v1", "apiKey": "peaproxy" }
    },
    "peaproxy-anthropic": {
      "npm": "@ai-sdk/anthropic",
      "options": { "baseURL": "http://127.0.0.1:8317/v1", "apiKey": "peaproxy" }
    }
  }
}
`,
		VerifyTODO: "peaproxy clients verify opencode",
	},
	"pi": {
		Name:       "pi",
		BaseURL:    "http://127.0.0.1:8317",
		AuthHeader: "depends on anthropic-messages vs OpenAI path",
		Notes:      "Do not apply Claude-Code cloak defaults to Pi.",
		Snippet: `# Pi — Anthropic-messages path (no cloak)
ANTHROPIC_BASE_URL=http://127.0.0.1:8317
ANTHROPIC_API_KEY=peaproxy
# Pi — OpenAI path
OPENAI_BASE_URL=http://127.0.0.1:8317/v1
OPENAI_API_KEY=peaproxy
`,
		VerifyTODO: "peaproxy clients verify pi",
	},
	"codex": {
		Name:       "codex",
		BaseURL:    "http://127.0.0.1:8317/v1",
		AuthHeader: "Authorization: Bearer peaproxy",
		Notes:      "Chat Completions work now. Responses API still TODO.",
		Snippet: `OPENAI_BASE_URL=http://127.0.0.1:8317/v1
OPENAI_API_KEY=peaproxy
`,
		VerifyTODO: "peaproxy clients verify codex",
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
		VerifyTODO: "peaproxy clients verify continue",
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
