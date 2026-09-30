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
	// Cloak is "off" (PeaProxy never injects Claude-Code cloak / clear_thinking)
	// or "opt-in" (Claude Code may enable cloak itself; PeaProxy still does not inject).
	Cloak      string
	Notes      string
	Snippet    string
	Verify     string
	VerifyTODO string
}

var presets = map[string]Preset{
	"cursor": {
		Name:       "cursor",
		BaseURL:    "http://127.0.0.1:8317/v1",
		AuthHeader: "Authorization: Bearer peaproxy",
		Cloak:      "off",
		Notes:      "OpenAI-compatible. Settings → Models → Override OpenAI Base URL. PeaProxy does not inject Claude-Code cloak or thinking.",
		Snippet: `OpenAI Base URL: http://127.0.0.1:8317/v1
API Key: peaproxy
`,
		VerifyTODO: "peaproxy clients verify cursor --chat",
	},
	"claude-code": {
		Name:       "claude-code",
		BaseURL:    "http://127.0.0.1:8317",
		AuthHeader: "x-api-key: peaproxy",
		Cloak:      "opt-in",
		Notes:      "Anthropic Messages. Claude Code appends /v1/messages — do NOT put /v1 on ANTHROPIC_BASE_URL (differs from OpenCode). Cloak / clear_thinking stay opt-in in Claude Code; PeaProxy never injects them.",
		Snippet: `# Claude Code — base URL WITHOUT /v1
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_API_KEY=peaproxy
`,
		VerifyTODO: "peaproxy clients verify claude-code --chat",
	},
	"opencode": {
		Name:       "opencode",
		BaseURL:    "http://127.0.0.1:8317/v1",
		AuthHeader: "Authorization: Bearer peaproxy",
		Cloak:      "off",
		Notes:      "baseURL INCLUDES /v1. Custom providers get no models.dev metadata: declare each model's limit, modalities and variants (copy them from `opencode models anthropic --verbose` / `openai`), or compaction never fires and effort variants are ignored. @ai-sdk/openai (Responses) is the only wire that carries GPT reasoning variants; @ai-sdk/openai-compatible suits other models. Keep the ids peaproxy-*: a provider named anthropic is also rewritten by opencode-claude-auth. Cloak defaults off.",
		Snippet: `{
  "provider": {
    "peaproxy-anthropic": {
      "npm": "@ai-sdk/anthropic",
      "options": { "baseURL": "http://127.0.0.1:8317/v1", "apiKey": "peaproxy" },
      "models": {
        "claude-opus-5-5": {
          "reasoning": true, "attachment": true, "tool_call": true,
          "modalities": { "input": ["text", "image", "pdf"], "output": ["text"] },
          "limit": { "context": 1000000, "output": 128000 },
          "variants": {
            "high": { "thinking": { "type": "adaptive" }, "effort": "high" },
            "max": { "thinking": { "type": "adaptive" }, "effort": "max" }
          }
        }
      }
    },
    "peaproxy-openai": {
      "npm": "@ai-sdk/openai",
      "options": { "baseURL": "http://127.0.0.1:8317/v1", "apiKey": "peaproxy" },
      "models": {
        "gpt-5.6-sol": {
          "reasoning": true, "attachment": true, "tool_call": true,
          "modalities": { "input": ["text", "image"], "output": ["text"] },
          "limit": { "context": 400000, "input": 272000, "output": 128000 },
          "variants": {
            "low": { "reasoningEffort": "low" },
            "high": { "reasoningEffort": "high" }
          }
        }
      }
    },
    "peaproxy-compat": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "http://127.0.0.1:8317/v1", "apiKey": "peaproxy" },
      "models": {
        "llama3.2": { "tool_call": true, "limit": { "context": 128000, "output": 8192 } }
      }
    }
  }
}
`,
		VerifyTODO: "peaproxy clients verify opencode --chat",
	},
	"pi": {
		Name:       "pi",
		BaseURL:    "http://127.0.0.1:8317",
		AuthHeader: "x-api-key (anthropic-messages) or Authorization: Bearer (openai-responses / openai-completions); PeaProxy accepts any value",
		Cloak:      "off",
		Notes:      "Pi reads endpoints from ~/.pi/agent/models.json only. connect pi honours PI_CODING_AGENT_DIR, writing $PI_CODING_AGENT_DIR/models.json when it is set, and <root>/.pi/agent/models.json otherwise. It does not read ANTHROPIC_BASE_URL or OPENAI_BASE_URL. Override the built-in anthropic (baseUrl WITHOUT /v1, the SDK appends /v1/messages) and openai (WITH /v1, Responses) providers so pi keeps its bundled model metadata: thinking levels, compat flags, cache lifetimes. apiKey is what makes the provider show in /model; a stored /login credential for the same provider takes precedence but still goes to PeaProxy. Models PeaProxy serves that pi's catalog lacks need a models entry with their own metadata (custom entries default to 128k context, no reasoning, text only). Put non-Claude/GPT catalog models under a peaproxy provider on openai-completions. /model reloads the file. `clients connect pi` writes the two overrides and ignores --model. PeaProxy cloak defaults are off (CLIProxyAPI #6120) — do not apply Claude-Code cloak / clear_thinking to Pi. verify --chat hits both wires.",
		Snippet: `{
  "providers": {
    "anthropic": { "baseUrl": "http://127.0.0.1:8317", "apiKey": "peaproxy",
      "models": [
        { "id": "claude-sonnet-5-5", "name": "Claude Sonnet 5.5", "reasoning": true, "input": ["text", "image"],
          "contextWindow": 1000000, "maxTokens": 128000,
          "thinkingLevelMap": { "off": null, "minimal": null, "low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "max" },
          "compat": { "forceAdaptiveThinking": true, "supportsTemperature": false, "supportsStrictTools": true } }
      ] },
    "openai": { "baseUrl": "http://127.0.0.1:8317/v1", "apiKey": "peaproxy" },
    "peaproxy": { "baseUrl": "http://127.0.0.1:8317/v1", "api": "openai-completions", "apiKey": "peaproxy",
      "models": [
        { "id": "REPLACE_WITH_CATALOG_ID", "reasoning": true, "input": ["text", "image"], "contextWindow": 1048576, "maxTokens": 65536 }
      ] }
  }
}
`,
		VerifyTODO: "peaproxy clients verify pi --chat",
	},
	"codex": {
		Name:       "codex",
		BaseURL:    "http://127.0.0.1:8317/v1",
		AuthHeader: "Authorization: Bearer peaproxy",
		Cloak:      "off",
		Notes:      "Codex CLI speaks the Responses API (wire_api=chat is gone). PeaProxy exposes POST /v1/responses. Codex OAuth passes tools/tool_choice/input items through (function_call, function_call_output, reasoning) and only strips stream_options (chatgpt.com rejects it) without reshuffling the rest of the JSON. Other adapters round-trip function tools via chat completions; they do not execute tools.",
		Snippet: `# ~/.codex/config.toml — do not reuse reserved provider ids openai/ollama/lmstudio.
model_provider = "peaproxy"
model = "REPLACE_WITH_CATALOG_ID"

[model_providers.peaproxy]
name = "PeaProxy"
base_url = "http://127.0.0.1:8317/v1"
env_key = "OPENAI_API_KEY"
wire_api = "responses"

# export OPENAI_API_KEY=peaproxy
`,
		VerifyTODO: "peaproxy clients verify codex --chat",
	},
	"continue": {
		Name:       "continue",
		BaseURL:    "http://127.0.0.1:8317/v1",
		AuthHeader: "Authorization: Bearer peaproxy",
		Cloak:      "off",
		Notes:      "Continue 1.x uses ~/.continue/config.yaml (schema v1). apiBase must include /v1. The older config.json models[].apiBase form still works on 0.x.",
		Snippet: `# ~/.continue/config.yaml
name: PeaProxy
version: 1.0.0
schema: v1
models:
  - name: PeaProxy
    provider: openai
    model: REPLACE_WITH_CATALOG_ID
    apiBase: http://127.0.0.1:8317/v1
    apiKey: peaproxy
`,
		VerifyTODO: "peaproxy clients verify continue --chat",
	},
	"cline": {
		Name:       "cline",
		BaseURL:    "http://127.0.0.1:8317/v1",
		AuthHeader: "Authorization: Bearer peaproxy",
		Cloak:      "off",
		Notes:      "Cline → API Provider → OpenAI Compatible (not the Cline cloud provider). Base URL must include /v1; Cline does not append it. Cline sends stream_options.include_usage on streams — PeaProxy forwards the body as-is.",
		Snippet: `Cline → Settings → API Provider: OpenAI Compatible
Base URL: http://127.0.0.1:8317/v1
API Key: peaproxy
Model ID: pick an id from GET /v1/models

# Cline concatenates {Base URL}/chat/completions — omit /v1 and the request misses /v1.
`,
		VerifyTODO: "peaproxy clients verify cline --chat",
	},
	"amp": {
		Name:       "amp",
		BaseURL:    "http://127.0.0.1:8317/v1",
		AuthHeader: "Authorization: Bearer peaproxy",
		Cloak:      "off",
		Notes:      "Do not set amp.url / AMP_URL to PeaProxy (that is Amp's management API). Use a Custom URL connection. Amp may send stream_options; chat/completions forwards as-is, Codex OAuth drops the key. PeaProxy does not speak Amp WebSocket or /api/provider/* routes.",
		Snippet: `# Amp → Custom URL connection (not amp.url)
# chat-completions (default): Amp appends /chat/completions
Base URL: http://127.0.0.1:8317/v1
API Key: peaproxy
Format: chat-completions

# optional Responses format (Amp appends /responses)
# Base URL: http://127.0.0.1:8317/v1
# Format: responses

# optional Anthropic format (Amp appends /v1/messages; do not double /v1)
# Base URL: http://127.0.0.1:8317
# Format: anthropic-messages
`,
		VerifyTODO: "peaproxy clients verify amp --chat",
	},
	"droid": {
		Name:       "droid",
		BaseURL:    "http://127.0.0.1:8317/v1",
		AuthHeader: "Authorization: Bearer peaproxy",
		Cloak:      "off",
		Notes:      "Factory Droid is a client of PeaProxy, not a chat-model upstream. Factory has no public consumer chat OAuth. Use BYOK generic-chat-completion-api (chat completions) or provider openai for Responses. Cloak defaults off.",
		Snippet: `# ~/.factory/settings.json — Droid BYOK pointing at PeaProxy
{
  "customModels": [
    {
      "model": "REPLACE_WITH_CATALOG_ID",
      "displayName": "PeaProxy (chat completions)",
      "baseUrl": "http://127.0.0.1:8317/v1",
      "apiKey": "peaproxy",
      "provider": "generic-chat-completion-api"
    },
    {
      "model": "REPLACE_WITH_CATALOG_ID",
      "displayName": "PeaProxy (Responses)",
      "baseUrl": "http://127.0.0.1:8317/v1",
      "apiKey": "peaproxy",
      "provider": "openai"
    }
  ]
}
`,
		VerifyTODO: "peaproxy clients verify droid --chat",
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
	if p.Verify == "" {
		p.Verify = p.VerifyTODO
	}
	return p, ok
}

func init() {
	for k, p := range presets {
		if p.Verify == "" {
			p.Verify = p.VerifyTODO
			presets[k] = p
		}
	}
}

// DefaultOrigin is the gateway address baked into the presets.
const DefaultOrigin = "http://127.0.0.1:8317"

// NormalizeOrigin reduces either form of a gateway address to the bare origin:
// the base URL with /v1 on it, and the plain origin, mean the same thing, and
// every command takes the same flag. Exactly one trailing /v1 is stripped, and
// only when it is the whole final segment, so a path like /v1beta is kept.
//
// This lives here so the CLI, the server and the verify path cannot drift
// apart over what --origin means.
func NormalizeOrigin(origin string) string {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		return ""
	}
	if rest, ok := strings.CutSuffix(origin, "/v1"); ok {
		return rest
	}
	return origin
}

// VerifyHint appends the --origin a verify command needs, unless the hint is
// empty or already points at the default.
func VerifyHint(hint, origin string) string {
	if hint == "" || origin == "" || origin == DefaultOrigin {
		return hint
	}
	return hint + " --origin " + origin
}

// WithOrigin returns a copy of p whose base URL and snippet point at origin
// instead of DefaultOrigin, with a verify hint that passes --origin.
func WithOrigin(p Preset, origin string) Preset {
	origin = NormalizeOrigin(origin)
	if origin == DefaultOrigin || origin == "" {
		return p
	}
	p.BaseURL = strings.ReplaceAll(p.BaseURL, DefaultOrigin, origin)
	p.Snippet = strings.ReplaceAll(p.Snippet, DefaultOrigin, origin)
	p.Verify = VerifyHint(p.Verify, origin)
	return p
}

// Format prints a preset for humans and agents.
func Format(p Preset) string {
	verify := p.Verify
	if verify == "" {
		verify = p.VerifyTODO
	}
	return fmt.Sprintf("name: %s\nbase_url: %s\nauth: %s\ncloak: %s\nnotes: %s\n\n# snippet\n%s\n# verify: %s\n",
		p.Name, p.BaseURL, p.AuthHeader, p.Cloak, p.Notes, p.Snippet, verify)
}
