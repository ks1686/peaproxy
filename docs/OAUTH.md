# OAuth

Karim (owner) overrode the prior “official OAuth only” policy on **2026-09-26**: PeaProxy ships **Claude Pro/Max** and **ChatGPT / Codex** subscription OAuth so a local gateway can reuse a subscription the user already pays for.

## Liability (read this)

Subscription OAuth through a local proxy **may violate Anthropic’s and OpenAI’s terms of service**. Using it can result in **account suspension or ban**, quota enforcement, or other loss of access.

**PeaProxy authors are not liable** for bans, suspensions, lost subscriptions, lost data, or any other damages. You proceed **at your own risk**.

The **official, lower-risk path** is an API key:

| Need | Adapter | Where |
|---|---|---|
| Claude Messages | `anthropic` | [console keys](https://console.anthropic.com/settings/keys), [docs](https://docs.anthropic.com/en/api/getting-started) |
| OpenAI platform | `openai` | [API keys](https://platform.openai.com/api-keys) |

Keep those presets in Accounts. Subscription OAuth is optional and clearly labeled as ban-risk.

## Status

| Adapter | Status | Upstream |
|---|---|---|
| `anthropic_oauth` | Implemented | Claude Code-style PKCE loopback (`claude.ai` → `platform.claude.com/v1/oauth/token`), then `api.anthropic.com/v1/messages` with `Authorization: Bearer` + `anthropic-beta: claude-code-20250219,oauth-2025-04-20` |
| `openai_oauth` | Implemented | Codex CLI PKCE loopback (`auth.openai.com`, callback `localhost:1455`) or `--device`, then Codex **Responses** at `https://chatgpt.com/backend-api/codex` |
| Gemini / xAI / Kimi / Qwen consumer OAuth | **Deferred** | Use the matching API-key adapters |

Live `ListModels` is still the catalog source of truth. Claude OAuth lists `/v1/models`. Codex lists `/models` on the Codex backend. If a subscription token cannot list a model, it will not appear until the provider returns it — there is no baked-in allowlist. Typical Claude Pro/Max ids and Codex GPT ids show up when the token is accepted.

## Login

CLI (preferred; completes the browser/device flow and writes `~/.config/peaproxy/config.yaml` mode 0600):

```bash
peaproxy auth login --provider anthropic
peaproxy auth login --provider openai
peaproxy auth login --provider openai --device   # no loopback port
peaproxy auth login --provider anthropic --print-url  # URL only, no wait
```

The command always prints the liability warning first. Tokens are never printed. Smoke after login:

```bash
peaproxy models list --filter subscription_oauth
# with peaproxy serve running:
curl -s http://127.0.0.1:8317/v1/models
# then a tiny chat against a listed id (Showcase also works)
```

Accounts UI: add the **Claude Pro/Max** or **ChatGPT / Codex** preset (warning is shown), then **OAuth login** (opens the provider URL and polls) or **Copy CLI**.

Callback ports (must match the public CLI OAuth clients):

- Claude: `http://localhost:54545/callback`
- Codex: `http://localhost:1455/auth/callback`

If the port is busy, paste the redirect URL into a later `AuthComplete` or use Codex `--device`.

## Storage and secrets

Tokens live under `providers[].oauth` in the YAML config (file mode **0600**). Refresh updates the same fields. The UI redacts tokens; request logs redact `bearer`, `sk-`, `x-api-key`, `access_token`, `refresh_token`, and `id_token`. Never log tokens.

## Architecture notes

- Adapters implement `Authenticator` (`AuthStart` / `AuthComplete`) plus `ListModels` / `Chat` / `ChatStream`. Claude OAuth also implements `NativeMessages`.
- JSON bodies used for Anthropic token exchange are structs (fixed key order), not `map[string]any`. Chat bodies still go through `jsonx.SetStream` (prompt-cache-safe).
- Failover, hide≠route, and catalog filters (`subscription_oauth`) are unchanged.
- PeaProxy does **not** vendor CLIProxyAPI. The flow was studied from [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (MIT) and reimplemented against PeaProxy’s adapter contract. Public Claude Code / Codex CLI client ids are used because those are the clients the subscription tokens are issued for.

## Residual gaps

- Gemini, xAI, Kimi, Qwen, and Copilot OAuth are not in this tree.
- Claude token endpoints sit behind Cloudflare; a stock `net/http` TLS fingerprint may get **403**. If login fails that way, use an official API key or retry from a normal desktop network.
- Codex chat is the **Responses** API, translated to OpenAI chat/completions locally. Native Codex `/responses` passthrough from clients is not a first-class PeaProxy route (OpenAI + Claude wires still are).
- No OS keychain yet (same as v0.1 keys: YAML 0600).
- Device-code is Codex-only.

## Manual smoke

1. `peaproxy auth login --provider anthropic` — complete browser login.
2. `peaproxy serve` and Catalog filter **Subscription OAuth**.
3. Showcase a listed Claude model (short prompt).
4. Repeat for `--provider openai` (Plus/Pro/Codex). Confirm `Chatgpt-Account-Id` is not logged.
5. Confirm `peaproxy auth login --provider anthropic --print-url` prints a PKCE URL without waiting.
