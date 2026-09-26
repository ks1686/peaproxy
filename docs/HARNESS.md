# Harness presets (stub)

`peaproxy clients` prints copy-ready snippets. **None of these are verified against a live gateway yet.** `peaproxy clients verify <name>` is an explicit no-op until the spike.

Default gateway: `http://127.0.0.1:8317`

| Client | Path | Notes |
|---|---|---|
| Cursor | `/v1` OpenAI-compat | Set OpenAI Base URL. Tool-call wire bugs in other proxies (#411-class) need golden tests. |
| Claude Code | Anthropic `/v1/messages` | Endpoint is **stubbed**. Do not expect chat. |
| OpenCode | `/v1` OpenAI-compat | Provider block in `opencode.json`. |
| Pi | Anthropic-messages **or** OpenAI | **Do not apply Claude-Code cloak defaults to Pi** (competitor #6120). Thinking injection must be a profile, not a global rewrite (#509). |
| Codex CLI / app | `/v1` + later Responses | Responses vs Messages quirks; image_gen tool conflicts. Not implemented. |
| Continue / Cline | `/v1` OpenAI-compat | Generic template. |

## Cursor (OpenAI-compat)

```
OpenAI Base URL: http://127.0.0.1:8317/v1
API Key: peaproxy
```

Loopback does not require a real key; LAN bind will.

## Claude Code

```
ANTHROPIC_BASE_URL=http://127.0.0.1:8317
ANTHROPIC_API_KEY=peaproxy
```

## OpenCode

```json
{
  "provider": {
    "peaproxy": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "http://127.0.0.1:8317/v1" }
    }
  }
}
```

## Pi

Use the Anthropic-messages path **or** the OpenAI path. Cloak / `clear_thinking` rewrites belong in a Claude Code profile only. Document the profile in code when the translator lands; until then the preset only prints URLs.

## Codex

```
OPENAI_BASE_URL=http://127.0.0.1:8317/v1
OPENAI_API_KEY=peaproxy
```

Responses API is a later wire. Do not claim Codex works.

## Verify (TODO)

```
peaproxy clients verify cursor
```

Intended smoke: `GET /v1/models` returns at least one exposed id; a tiny chat round-trip if an adapter is healthy. Not implemented in the scaffold.
