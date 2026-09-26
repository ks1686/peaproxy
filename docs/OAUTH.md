# OAuth

Karim (owner) overrode the prior “official OAuth only” policy on **2026-09-26**: PeaProxy ships **consumer subscription OAuth** so a local gateway can reuse a subscription the user already pays for. First verticals were Claude Pro/Max and ChatGPT/Codex; this tree also includes Gemini/Antigravity, xAI Grok, Kimi, and Meta Muse.

## Liability (read this)

Subscription OAuth through a local proxy **may violate the provider’s terms of service**. Using it can result in **account suspension or ban**, quota enforcement, or other loss of access.

**PeaProxy authors are not liable** for bans, suspensions, lost subscriptions, lost data, or any other damages. You proceed **at your own risk**. This applies to **every** subscription OAuth path below.

The **official, lower-risk path** is an API key:

| Need | Adapter | Where |
|---|---|---|
| Claude Messages | `anthropic` | [console keys](https://console.anthropic.com/settings/keys), [docs](https://docs.anthropic.com/en/api/getting-started) |
| OpenAI platform | `openai` | [API keys](https://platform.openai.com/api-keys) |
| Gemini (AI Studio) | `google` / `gemini` | [AI Studio keys](https://aistudio.google.com/apikey), [OpenAI-compat docs](https://ai.google.dev/gemini-api/docs/openai) |
| xAI Grok | `xai` | [xAI console](https://console.x.ai/) |

Keep those presets in Accounts. Subscription OAuth is optional and clearly labeled as ban-risk.

## Status

Flows were studied from [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (MIT) and **reimplemented** in PeaProxy. CPA is not vendored.

| Adapter | Status | Upstream |
|---|---|---|
| `anthropic_oauth` | Implemented | Claude Code-style PKCE loopback (`claude.ai` → `platform.claude.com/v1/oauth/token`), then `api.anthropic.com/v1/messages` with `Authorization: Bearer` + `anthropic-beta: claude-code-20250219,oauth-2025-04-20` |
| `openai_oauth` | Implemented | Codex CLI PKCE loopback (`auth.openai.com`, callback `localhost:1455`) or `--device`, then Codex **Responses** at `https://chatgpt.com/backend-api/codex` |
| `antigravity` (alias `gemini_oauth`) | Implemented | Google OAuth loopback for the public Antigravity IDE client (`localhost:51121/oauth-callback`), Cloud Code `loadCodeAssist` for a GCP project, then `v1internal:fetchAvailableModels` / `generateContent`. **Distinct** from AI Studio API keys. |
| `xai_oauth` | Implemented | RFC 8628 device code via `auth.x.ai` OIDC discovery, then OpenAI-compat chat at `https://cli-chat-proxy.grok.com/v1` |
| `kimi_oauth` | Implemented | Device code at `auth.kimi.com`, OpenAI-compat at `https://api.kimi.com/coding/v1` |
| `kimi_ai_oauth` | Implemented | Same flow on `kimi.ai` (`auth.kimi.ai` / `api.kimi.ai/coding/v1`) |
| `meta_oauth` | Implemented | Device code at `auth.meta.com`, mint `api.meta.ai/muse-code/key`, then OpenAI-compat at `https://api.meta.ai/v1` |
| `qwen_oauth` | **Not yet** | CPA has no working Qwen consumer OAuth. Use a Qwen API key with `openai_compat`. |
| Devin | Skipped | CPA has a session-token login; it is not a generic chat-model upstream for this gateway. |
| Vertex | Skipped | Service-account / GCP credentials, not consumer subscription OAuth. |

Live `ListModels` is still the catalog source of truth. If a subscription token cannot list a model, it will not appear — there is no baked-in allowlist.

## Login

CLI (preferred; completes the browser/device flow and writes `~/.config/peaproxy/config.yaml` mode 0600):

```bash
peaproxy auth login --provider anthropic
peaproxy auth login --provider openai
peaproxy auth login --provider openai --device   # no loopback port
peaproxy auth login --provider gemini            # Antigravity / Gemini consumer
peaproxy auth login --provider antigravity       # same adapter
peaproxy auth login --provider xai
peaproxy auth login --provider kimi
peaproxy auth login --provider kimi-ai
peaproxy auth login --provider meta
peaproxy auth login --provider anthropic --print-url  # URL only, no wait
```

The command always prints the liability warning first. Tokens are never printed. Smoke after login:

```bash
peaproxy models list --filter subscription_oauth
# with peaproxy serve running:
curl -s http://127.0.0.1:8317/v1/models
# then a tiny chat against a listed id (Showcase also works)
```

Accounts UI: add a subscription OAuth preset (warning is shown), then **OAuth login** (opens the provider URL and polls) or **Copy CLI**.

Callback ports (must match the public CLI OAuth clients):

- Claude: `http://localhost:54545/callback`
- Codex: `http://localhost:1455/auth/callback`
- Gemini / Antigravity: `http://localhost:51121/oauth-callback`

xAI, Kimi, and Meta Muse use **device code** (no loopback port). If a PKCE port is busy, paste the redirect URL into a later `AuthComplete` or use Codex `--device`.

## Storage and secrets

Tokens live under `providers[].oauth` in the YAML config (file mode **0600**), including optional `extra` keys (`project_id`, `device_id`, `dca_token`, `token_endpoint`). Refresh updates the same fields. The UI redacts tokens; request logs redact `bearer`, `sk-`, `x-api-key`, `access_token`, `refresh_token`, and `id_token`. Never log tokens.

## Architecture notes

- Adapters implement `Authenticator` (`AuthStart` / `AuthComplete`) plus `ListModels` / `Chat` / `ChatStream`. Claude OAuth also implements `NativeMessages`.
- JSON bodies used for Anthropic token exchange are structs (fixed key order), not `map[string]any`. Chat bodies still go through `jsonx.SetStream` (prompt-cache-safe).
- Failover, hide≠route, and catalog filters (`subscription_oauth`) are unchanged.
- PeaProxy does **not** vendor CLIProxyAPI. Public CLI client ids (Claude Code, Codex CLI, Antigravity IDE, Grok CLI, Kimi Code, Muse CLI) are used because those are the clients the subscription tokens are issued for.

## Residual gaps

- Qwen consumer OAuth is stubbed **not yet** (no CPA flow).
- Devin and Vertex are intentionally omitted (not generic consumer chat OAuth).
- Claude token endpoints sit behind Cloudflare; a stock `net/http` TLS fingerprint may get **403**. If login fails that way, use an official API key or retry from a normal desktop network.
- Codex chat is the **Responses** API, translated to OpenAI chat/completions locally. Native Codex `/responses` passthrough from clients is not a first-class PeaProxy route (OpenAI + Claude wires still are).
- Antigravity chat is Cloud Code `generateContent`, translated to OpenAI chat locally. No uTLS / HTTP/2 fingerprint matching vs the native Antigravity binary.
- No OS keychain yet (same as v0.1 keys: YAML 0600).

## Manual smoke

1. `peaproxy auth login --provider anthropic` — complete browser login.
2. `peaproxy serve` and Catalog filter **Subscription OAuth**.
3. Showcase a listed Claude model (short prompt).
4. Repeat for `--provider openai` (Plus/Pro/Codex). Confirm `Chatgpt-Account-Id` is not logged.
5. Repeat for `--provider gemini`, then `--provider xai` (device code).
6. Confirm `peaproxy auth login --provider anthropic --print-url` prints a PKCE URL without waiting.
