# Harness presets

Copy-ready client configs. Product status: [PLAN.md](PLAN.md), [V1.md](V1.md). Adapters: [PROVIDERS.md](PROVIDERS.md).

`peaproxy clients show <name>` prints copy-ready snippets. `peaproxy clients detect`, `connect`, `disconnect`, and `status` edit only the PeaProxy block in Pi, Continue, Codex, and Claude Code configs (Claude Code: the `ANTHROPIC_*` keys in `settings.json` `env`). `connect opencode` is guided and prints the preset below, since a working OpenCode provider needs per-model metadata. Pass `--root` with a temporary directory so those commands do not touch the real home directory. `verify` is a protocol probe against a running gateway, not proof that an installed harness launched.

`peaproxy clients verify <name>` GETs `http://127.0.0.1:8317/v1/models` (serve must be running). Add `--chat` to POST a tiny completion on the preset’s wire (`/v1/chat/completions` for Cursor/OpenCode/Continue/Cline/Amp/Droid, `/v1/messages` for `claude-code`, `/v1/responses` for `codex`). `pi --chat` hits **both** OpenAI chat and Anthropic messages. `--origin` overrides the gateway URL.

Default gateway: `http://127.0.0.1:8317`. Connect from the web UI or admin API writes the address the running server listens on, and `GET /admin/clients` snippets and verify hints use that port (the hint adds `--origin` off the default). The CLI's `clients show`, `clients connect`, and `verify` default to that address, so pass `--origin` when the server runs elsewhere. `--origin` means the same thing on all three: the bare gateway origin. A trailing `/v1` is accepted and normalized away, and `connect` then gives each client the suffix its own wire needs — `/v1` for the OpenAI-shaped clients (codex, continue, pi), none for `claude-code`, which appends `/v1/messages` itself. Catalog pin/rename/hide, request-log tail, and richer health also live on the CLI (`peaproxy catalog pin|rename|hide`, `peaproxy requests tail`, `peaproxy health` matching `GET /admin/health` including quota remaining; `peaproxy accounts add <preset>` for Jan/GPT4All/SambaNova/Workers AI).

**Harness cloak defaults stay off.** Unlike CLIProxyAPI (#6120), PeaProxy does **not** inject Claude-Code thinking / `clear_thinking` into client presets (Pi, Cursor, OpenCode, …). Claude Code may enable cloak itself (`cloak: opt-in` on that preset only).

**Separate (adapter, not a client preset):** `anthropic_oauth` Messages to Anthropic **do** inject Claude Code’s billing header + CLI identity system blocks (v1.6.8/v1.6.9) so subscription OAuth is not 429’d as a non-CLI client. Caller system text is relocated, never deleted. Official `adapter: anthropic` API keys are not cloaked this way.

**OpenCode and Claude Code do not share the same Anthropic base URL.** Claude Code typically wants `ANTHROPIC_BASE_URL` *without* `/v1` (it appends `/v1/messages`). OpenCode's Anthropic provider often wants `baseURL` *including* `/v1` ([anomalyco/opencode#35005](https://github.com/anomalyco/opencode/issues/35005)).

**Non-Claude models on `/v1/messages`** stream tool calls as `tool_use` content blocks and carry usage in `message_delta`. Each call's arguments arrive as one complete `input_json_delta` after the text block closes; while they buffer, a `ping` event is sent after each second of silence so idle timeouts do not fire. Truncation is reported as `stop_reason: max_tokens`. Antigravity Gemini works with OpenCode tools on either wire.

| Client | Wire | Base URL | Cloak | Gotchas |
|---|---|---|---|---|
| Cursor | OpenAI chat completions | `http://127.0.0.1:8317/v1` | off | Override OpenAI Base URL |
| Claude Code | Anthropic Messages | `http://127.0.0.1:8317` (**no** `/v1`) | opt-in (client-side only) | Preset does not inject thinking. `anthropic_oauth` upstream still applies system cloak |
| OpenCode | Anthropic, OpenAI **Responses**, OpenAI-compat | all use `.../v1` | off | Custom providers need per-model `limit` / `variants`; GPT on `@ai-sdk/openai`; ids must not be `anthropic` / `openai` |
| Pi | Anthropic Messages **and** OpenAI Responses / chat | `~/.pi/agent/models.json` only; `anthropic`: no `/v1`, `openai`: `/v1` | off | Env base URLs are not read. Override the built-in providers to keep pi's model metadata |
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

A custom OpenCode provider gets **no models.dev metadata**, so a provider block with only `npm` + `options` produces models that OpenCode treats as having no context limit and no variants. Three things follow (all live-verified, 2026-09-29):

- **Declare each model.** Without `limit` compaction never fires, and without `variants` the effort picker is empty. Copy `limit`, `modalities` and `variants` from OpenCode's own catalog (`opencode models anthropic --verbose`, `opencode models openai --verbose`) and trim to the models you use.
- **GPT goes through `@ai-sdk/openai` (Responses).** It is the only wire that carries `reasoningEffort` variants to Codex. `@ai-sdk/openai-compatible` (chat completions) is right for everything else, including Antigravity Gemini.
- **Do not name a provider `anthropic`, `openai`, or `google`.** With `opencode-claude-auth` (or another auth plugin) installed, a provider under the built-in id is also rewritten by the plugin. `peaproxy-*` ids keep the plugins out of the way and leave the direct providers usable.

Where your model name differs from the id PeaProxy serves, add `"id"` on the model (for example `"claude-haiku-4-5": { "id": "claude-haiku-4-5-20251001", ... }`); no PeaProxy catalog route is needed. Unknown top-level keys in `opencode.json` are ignored silently, so a typo is not reported.

```json
{
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
```

If you run oh-my-opencode, its image resizer only runs for the built-in `anthropic` provider, so a very large screenshot sent through `peaproxy-anthropic` reaches Anthropic at full size and can hit its image limit.

## Pi (models.json)

Pi (`pi-coding-agent`) takes provider endpoints from **`~/.pi/agent/models.json`**. Pi honours `PI_CODING_AGENT_DIR`, and `connect pi` honours it too: when it is set, the provider list is written to `$PI_CODING_AGENT_DIR/models.json`, otherwise to `<root>/.pi/agent/models.json`. It does **not** read `ANTHROPIC_BASE_URL` or `OPENAI_BASE_URL` — in pi 0.87.1 the only base URLs read from the environment are Azure's and Cloudflare's — so exporting those configures nothing. The earlier env-export preset was wrong.

The shortest working setup **overrides the built-in providers**. Pi then keeps its bundled metadata for every Claude and GPT model (thinking-level maps, compat flags, prompt-cache lifetimes) and only the URL changes:

```json
{
  "providers": {
    "anthropic": { "baseUrl": "http://127.0.0.1:8317", "apiKey": "peaproxy" },
    "openai":    { "baseUrl": "http://127.0.0.1:8317/v1", "apiKey": "peaproxy" }
  }
}
```

`peaproxy clients connect pi` writes exactly those two overrides (merging into an existing `providers.anthropic` / `providers.openai` entry and leaving its other fields alone); `disconnect pi` removes `baseUrl` + `apiKey` only from entries whose `apiKey` is `peaproxy`. `--model` is ignored for pi because the whole catalog routes through. Then pick `anthropic/<id>` or `openai/<id>` in `/model` as usual.

- **`anthropic` has no `/v1`** — pi's Anthropic SDK client appends `/v1/messages`. **`openai` includes `/v1`** — the built-in provider is `openai-responses` and appends `/responses`, which lands on PeaProxy's Codex-OAuth-native wire.
- **`apiKey` is what makes a provider appear in `/model`.** PeaProxy ignores the value. If you also `/login`ed the same provider inside pi, that stored credential wins over `models.json` (pi's order: `--api-key`, `auth.json`, `models.json`, environment) but the request still goes to `baseUrl`, so PeaProxy gets it either way; `/logout anthropic` just stops pi sending its own token.
- **A model PeaProxy serves but pi's catalog lacks** (Sonnet 5.5 in 0.87.1, for example) needs a `models` entry with its own metadata. A custom entry inherits only `api` and `baseUrl`; it defaults to `contextWindow` 128000, `maxTokens` 16384, `reasoning: false`, text-only.
- **Everything else in the catalog** (Antigravity Gemini, Kimi, local models) goes under a custom provider on `openai-completions`. Same metadata rule.
- `/model` reloads the file; no restart.

`peaproxy clients show pi` prints the full form, with one example of each:

```json
{
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
```

Live-verified 2026-09-29 with pi 0.87.1 against PeaProxy on an Enterprise Claude OAuth account and a Codex OAuth account: tool-call turns (`read`) on `anthropic/claude-opus-5-5`, the custom `anthropic/claude-sonnet-5-5` entry at `--thinking medium`, `openai/gpt-5.6-sol`, and `peaproxy/claude-haiku-4-5-20251001` over `openai-completions`. Pi's lowercase `read` / `bash` / `edit` / `write` tools are already aliased to Claude Code's names on the `anthropic_oauth` lane ([OAUTH.md](OAUTH.md)), so a Team/Enterprise workspace does not reject them as a third-party app.

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
