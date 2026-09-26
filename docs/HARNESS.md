# Harness presets

Copy-ready client configs. Product status: [PLAN.md](PLAN.md), [V1.md](V1.md). Adapters: [PROVIDERS.md](PROVIDERS.md).

`peaproxy clients show <name>` prints copy-ready snippets.

`peaproxy clients verify <name>` GETs `http://127.0.0.1:8317/v1/models` (serve must be running). Add `--chat` to POST a tiny completion on the preset’s wire (`/v1/chat/completions` for Cursor/OpenCode/Continue/Cline/Amp, `/v1/messages` for `claude-code`, `/v1/responses` for `codex`). `pi --chat` hits **both** OpenAI chat and Anthropic messages. `--origin` overrides the gateway URL.

Default gateway: `http://127.0.0.1:8317`

**PeaProxy cloak defaults are off.** Unlike CLIProxyAPI (#6120), PeaProxy never injects Claude-Code cloak headers or `clear_thinking`. Claude Code may enable cloak itself (`cloak: opt-in` on that preset only).

**OpenCode and Claude Code do not share the same Anthropic base URL.** Claude Code typically wants `ANTHROPIC_BASE_URL` *without* `/v1` (it appends `/v1/messages`). OpenCode's Anthropic provider often wants `baseURL` *including* `/v1` ([anomalyco/opencode#35005](https://github.com/anomalyco/opencode/issues/35005)).

| Client | Wire | Base URL | Cloak | Gotchas |
|---|---|---|---|---|
| Cursor | OpenAI chat completions | `http://127.0.0.1:8317/v1` | off | Override OpenAI Base URL |
| Claude Code | Anthropic Messages | `http://127.0.0.1:8317` (**no** `/v1`) | opt-in (client-side only) | PeaProxy does not inject thinking/cloak |
| OpenCode | OpenAI-compat **and** Anthropic | both use `.../v1` | off | Separate snippet from Claude Code |
| Pi | Anthropic **and** OpenAI (both documented) | Anthropic: no `/v1`; OpenAI: includes `/v1` | off | Do not apply Claude-Code cloak defaults |
| Codex | OpenAI **Responses** (`POST /v1/responses`) | `.../v1` | off | `wire_api = "responses"` only. Codex OAuth strips `stream_options` |
| Continue | OpenAI-compat | `.../v1` | off | `apiBase` in `~/.continue/config.yaml` |
| Cline | OpenAI Compatible provider | `.../v1` | off | Must include `/v1`; Cline sends `stream_options` (forwarded) |
| Amp | Custom URL `chat-completions` | `.../v1` | off | **Do not** set `amp.url` / `AMP_URL` to PeaProxy. No Amp WebSocket |

## Cursor

```
OpenAI Base URL: http://127.0.0.1:8317/v1
API Key: peaproxy
```

## Claude Code (no /v1)

```
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_API_KEY=peaproxy
```

## OpenCode (includes /v1)

```json
{
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
```

## Pi (both wires)

```
# Anthropic-messages (NO /v1; Pi appends /v1/messages)
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_API_KEY=peaproxy

# OpenAI chat-completions (includes /v1)
export OPENAI_BASE_URL=http://127.0.0.1:8317/v1
export OPENAI_API_KEY=peaproxy
```

PeaProxy cloak defaults are **off** (CLIProxyAPI #6120). Do not inject Claude-Code cloak / `clear_thinking` for Pi. `peaproxy clients verify pi --chat` covers both wires.

## Continue

Continue 1.x reads YAML (not the old `config.json` `models[]` blob):

```yaml
# ~/.continue/config.yaml
name: PeaProxy
version: 1.0.0
schema: v1
models:
  - name: PeaProxy
    provider: openai
    model: REPLACE_WITH_CATALOG_ID
    apiBase: http://127.0.0.1:8317/v1
    apiKey: peaproxy
```

`apiBase` must include `/v1`.

## Cline

```
Cline → Settings → API Provider: OpenAI Compatible
Base URL: http://127.0.0.1:8317/v1
API Key: peaproxy
Model ID: pick an id from GET /v1/models
```

Do **not** pick the Cline cloud provider. Cline concatenates `{Base URL}/chat/completions` — omit `/v1` and the request misses `/v1`. Cline sends `stream_options.include_usage` on streams; PeaProxy forwards the JSON as-is (prompt-cache-safe, no map round-trip).

## Amp

Do **not** set `amp.url` or `AMP_URL` to PeaProxy. That field is Amp's management API (`ampcode.com`), not an OpenAI-compatible proxy. VibeProxy users who pointed `amp.url` at `:8317` got credit/outage failures.

Use Amp **Custom URL**:

```
# Amp → Custom URL connection (not amp.url)
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
```

Amp may send `stream_options`. Chat Completions forwards it. Codex OAuth (`openai_oauth`) drops `stream_options` because `chatgpt.com` Codex `/responses` rejects it. PeaProxy does not speak Amp WebSocket or `/api/provider/*` namespaced routes.

## Codex (Responses)

Codex CLI no longer supports `wire_api = "chat"`. Point a **custom** provider at PeaProxy (do not reuse reserved ids `openai`, `ollama`, or `lmstudio`):

```
# ~/.codex/config.toml
model_provider = "peaproxy"
model = "REPLACE_WITH_CATALOG_ID"

[model_providers.peaproxy]
name = "PeaProxy"
base_url = "http://127.0.0.1:8317/v1"
env_key = "OPENAI_API_KEY"
wire_api = "responses"
```

```
export OPENAI_API_KEY=peaproxy
```

`POST /v1/responses` is first-class. ChatGPT/Codex subscription OAuth (`openai_oauth`) passes the body through to Codex `/responses` after a surgical `stream_options` drop. Other adapters are translated through chat completions (text in / text out; enough for a smoke, not a full Responses tools surface).

## Verify

```
peaproxy serve
peaproxy clients verify cursor --chat
peaproxy clients verify opencode --chat
peaproxy clients verify claude-code --chat
peaproxy clients verify pi --chat
peaproxy clients verify codex --chat
peaproxy clients verify continue --chat
peaproxy clients verify cline --chat
peaproxy clients verify amp --chat
```
