# Harness presets

Copy-ready client configs. Product status: [PLAN.md](PLAN.md), [V1.md](V1.md). Adapters: [PROVIDERS.md](PROVIDERS.md).

`peaproxy clients show <name>` prints copy-ready snippets.

`peaproxy clients verify <name>` GETs `http://127.0.0.1:8317/v1/models` (serve must be running). Add `--chat` to POST a tiny completion on the preset’s wire (`/v1/chat/completions` for Cursor/OpenCode/Continue/Cline/Amp/Droid, `/v1/messages` for `claude-code`, `/v1/responses` for `codex`). `pi --chat` hits **both** OpenAI chat and Anthropic messages. `--origin` overrides the gateway URL.

Default gateway: `http://127.0.0.1:8317`. Catalog pin/rename/hide, request-log tail, and richer health also live on the CLI (`peaproxy catalog pin|rename|hide`, `peaproxy requests tail`, `peaproxy health` matching `GET /admin/health` including quota remaining; `peaproxy accounts add <preset>` for Jan/GPT4All/SambaNova/Workers AI).

**Harness cloak defaults stay off.** Unlike CLIProxyAPI (#6120), PeaProxy does **not** inject Claude-Code thinking / `clear_thinking` into client presets (Pi, Cursor, OpenCode, …). Claude Code may enable cloak itself (`cloak: opt-in` on that preset only).

**Separate (adapter, not a client preset):** `anthropic_oauth` Messages to Anthropic **do** inject Claude Code’s billing header + CLI identity system blocks (v1.6.8/v1.6.9) so subscription OAuth is not 429’d as a non-CLI client. Caller system text is relocated, never deleted. Official `adapter: anthropic` API keys are not cloaked this way.

**OpenCode and Claude Code do not share the same Anthropic base URL.** Claude Code typically wants `ANTHROPIC_BASE_URL` *without* `/v1` (it appends `/v1/messages`). OpenCode's Anthropic provider often wants `baseURL` *including* `/v1` ([anomalyco/opencode#35005](https://github.com/anomalyco/opencode/issues/35005)).

| Client | Wire | Base URL | Cloak | Gotchas |
|---|---|---|---|---|
| Cursor | OpenAI chat completions | `http://127.0.0.1:8317/v1` | off | Override OpenAI Base URL |
| Claude Code | Anthropic Messages | `http://127.0.0.1:8317` (**no** `/v1`) | opt-in (client-side only) | Preset does not inject thinking. `anthropic_oauth` upstream still applies system cloak |
| OpenCode | OpenAI-compat **and** Anthropic | both use `.../v1` | off | Separate snippet from Claude Code |
| Pi | Anthropic **and** OpenAI (both documented) | Anthropic: no `/v1`; OpenAI: includes `/v1` | off | Do not apply Claude-Code cloak defaults to the Pi preset |
| Codex | OpenAI **Responses** (`POST /v1/responses`) | `.../v1` | off | `wire_api = "responses"` only. Codex OAuth forces `store: false`; omits `stream_options` / `max_output_tokens` |
| Continue | OpenAI-compat | `.../v1` | off | `apiBase` in `~/.continue/config.yaml` |
| Cline | OpenAI Compatible provider | `.../v1` | off | Must include `/v1`; Cline sends `stream_options` (forwarded) |
| Amp | Custom URL `chat-completions` | `.../v1` | off | **Do not** set `amp.url` / `AMP_URL` to PeaProxy. No Amp WebSocket |
| Droid | Factory BYOK | `.../v1` | off | `generic-chat-completion-api` for chat; `provider: openai` for Responses. Factory is **not** a chat-model upstream |

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

Client-preset cloak defaults are **off** (CLIProxyAPI #6120). Do not inject Claude-Code thinking / `clear_thinking` for Pi. `anthropic_oauth` still applies a non-strict upstream system cloak (caller system kept). `peaproxy clients verify pi --chat` covers both wires.

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

Amp may send `stream_options`. Chat Completions forwards it. Codex OAuth (`openai_oauth`) drops `stream_options` and `max_output_tokens` and forces `store: false` because `chatgpt.com` Codex `/responses` rejects the rest. PeaProxy does not speak Amp WebSocket or `/api/provider/*` namespaced routes.

## Factory Droid (BYOK client)

Factory Droid is a **coding harness**, not a PeaProxy chat-model adapter. Factory has **no public consumer chat OAuth**. Point Droid at PeaProxy with BYOK in `~/.factory/settings.json`:

```json
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
```

`provider: "generic-chat-completion-api"` hits `POST /v1/chat/completions`. `provider: "openai"` hits Droid's Responses path (`POST /v1/responses`). Pick a live id from `GET /v1/models`. Official Factory API keys at `https://api.factory.ai` are for sessions/CI/computers — they are **not** an OpenAI-compat chat upstream.

`peaproxy clients show droid` / `peaproxy clients verify droid --chat`.

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

`POST /v1/responses` is first-class. ChatGPT/Codex subscription OAuth (`openai_oauth`) passes the body through to Codex `/responses` after forcing `store: false` and dropping `stream_options` / `max_output_tokens` — including `tools`, `tool_choice`, and input items (`function_call`, `function_call_output`, `reasoning`). Chat Completions clients on a Codex OAuth account map tools / `tool_calls` / `tool` messages into those items (and map function_call outputs back to `tool_calls`). Translated chat SSE emits `finish_reason` before `[DONE]`. Cross-wire thinking and reasoning travel on the chat assistant field `reasoning_opaque` (`anthropic_thinking`, `anthropic_redacted_thinking`, `responses_reasoning`). Kinds are not rewritten into each other. Visible `content` stays the answer text. OpenAI-compat upstreams strip the field before the POST. A client that drops unknown assistant fields will not echo the signature. Other adapters translate via chat completions and round-trip function tools one level (request `tools` + `tool_calls` → Responses `function_call` items). That path does **not** execute tools or synthesize a full Responses tool event stream. `image_gen` is not a `/v1/images/generations` proxy.

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
peaproxy clients verify droid --chat
```
