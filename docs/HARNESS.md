# Harness presets

`peaproxy clients show <name>` prints copy-ready snippets.

`peaproxy clients verify <name>` GETs `http://127.0.0.1:8317/v1/models` (serve must be running). Add `--chat` to POST a tiny completion on the preset’s wire (`/v1/chat/completions`, or `/v1/messages` for `claude-code`). `--origin` overrides the gateway URL.

Default gateway: `http://127.0.0.1:8317`

**OpenCode and Claude Code do not share the same Anthropic base URL.** Claude Code typically wants `ANTHROPIC_BASE_URL` *without* `/v1` (it appends `/v1/messages`). OpenCode's Anthropic provider often wants `baseURL` *including* `/v1` ([anomalyco/opencode#35005](https://github.com/anomalyco/opencode/issues/35005)).

| Client | Wire | Base URL | Gotchas |
|---|---|---|---|
| Cursor | OpenAI chat completions | `http://127.0.0.1:8317/v1` | Override OpenAI Base URL |
| Claude Code | Anthropic Messages | `http://127.0.0.1:8317` (**no** `/v1`) | Cloak/thinking must stay opt-in per profile |
| OpenCode | OpenAI-compat **and** Anthropic | both use `.../v1` | Separate snippet from Claude Code |
| Pi | Anthropic **and** OpenAI (both documented) | Anthropic: no `/v1`; OpenAI: includes `/v1` | Do not apply Claude-Code cloak defaults |
| Codex | OpenAI chat completions | `.../v1` | Responses API still TODO |
| Continue | OpenAI-compat | `.../v1` | `apiBase` in Continue config |
| Cline | OpenAI Compatible provider | `.../v1` | Settings → API Provider |

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

Do not inject Claude-Code cloak / `clear_thinking` for Pi.

## Continue

```json
{
  "models": [{
    "title": "PeaProxy",
    "provider": "openai",
    "model": "REPLACE_WITH_CATALOG_ID",
    "apiBase": "http://127.0.0.1:8317/v1",
    "apiKey": "peaproxy"
  }]
}
```

## Cline

```
Cline → Settings → API Provider: OpenAI Compatible
Base URL: http://127.0.0.1:8317/v1
API Key: peaproxy
```

## Verify

```
peaproxy serve
peaproxy clients verify cursor
peaproxy clients verify claude-code --chat
peaproxy clients verify pi --chat
```
