# PeaProxy

Local multi-provider AI gateway in Go: **API keys + free/local providers + subscription OAuth** (Claude, Codex, Gemini/Antigravity, xAI, Kimi, Meta Muse; ToS/ban risk; API keys remain the official path), first-class text + images, a **live auto model catalog** (no hand-maintained allowlist), OpenAI- and Claude-compatible endpoints, a CLI service, and a localhost UI.

Status: **v0.2.0** — subscription OAuth is in, tokens and inline API keys go to the **OS keychain** (encrypted file fallback). Qwen consumer OAuth remains stubbed. **ToS/ban risk is unchanged: authors are not liable; prefer official API keys.**

## One-liner

Maximize whatever you already pay for (Claude Pro/Max, ChatGPT/Codex, Gemini/Antigravity, xAI, Kimi, Meta Muse subscription OAuth, **at your own risk**), fall back to keys, and treat Ollama / LM Studio / OpenRouter-free / OpenCode Zen as first-class — then expose one OpenAI-shaped and one Claude-shaped local endpoint that coding tools already speak.

## Install

```bash
go install github.com/ks1686/peaproxy/cmd/peaproxy@latest
peaproxy --version
peaproxy serve
```

Requires Go 1.22+. Tagged releases (`v*`) also publish linux/darwin/windows **amd64 + arm64** binaries via GoReleaser (GitHub Releases).

```bash
go run ./cmd/peaproxy serve   # from a clone
```

Open http://127.0.0.1:8317/

![Accounts](docs/screenshots/accounts.png)
![Catalog](docs/screenshots/catalog.png)
![Showcase](docs/screenshots/showcase.png)
![Health](docs/screenshots/health.png)

## Quick start

1. **Accounts** — pick a preset (Ollama, LM Studio, Groq, Cerebras, Google AI Studio / Gemini **key**, xAI **key**, Hugging Face, Anthropic **API key**, OpenAI **API key**, subscription OAuth for Claude / Codex / Gemini-Antigravity / xAI / Kimi / Meta Muse, OpenRouter, OpenCode Zen, or custom OpenAI-compat). OAuth presets show a ban-risk warning; prefer keys.
2. **Catalog** — live `ListModels`. Hide is listing-only (CPA #5995). Free/Paid/Local filter is remembered in the UI.
3. **Showcase** — try a prompt; vision models accept an image URL or upload.
4. Point Cursor / OpenCode / Claude Code / Pi / Continue / Cline at the local base URL (`peaproxy clients show …`).

```bash
curl -s http://127.0.0.1:8317/v1/models
curl -s http://127.0.0.1:8317/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"llama3.2","messages":[{"role":"user","content":"hi"}]}'
curl -s http://127.0.0.1:8317/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{"model":"llama3.2","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hi"}]}'
peaproxy clients show cursor
peaproxy clients show opencode   # includes /v1
peaproxy clients show claude-code  # does NOT include /v1
peaproxy clients show pi         # both wires
peaproxy clients verify cursor --chat
```

First `serve` writes `~/.config/peaproxy/config.yaml` if missing. Example: [configs/peaproxy.example.yaml](configs/peaproxy.example.yaml). Env overlays: [docs/CONFIG.md](docs/CONFIG.md). Usage is persisted as `usage.json` next to the config; set `requestLog: true` for a redacted `requests.log`. OAuth tokens and inline API keys are stored in the OS keychain, or an encrypted file next to the YAML when no keychain is available. YAML still lists accounts (email, adapter) without printing secrets.

| Command | Purpose |
|---|---|
| `serve` | Listen `127.0.0.1:8317` + UI (writes first-run config) |
| `--version` | Build version (`dev` unless a release ldflag) |
| `auth` | Subscription OAuth (`--provider anthropic\|openai\|gemini\|xai\|kimi\|kimi-ai\|meta`). Prints a ToS/ban-risk warning. `--print-url` / `--device` / `--no-browser`. Prefer API keys. Qwen is stubbed not-yet. |
| `accounts` | Configured provider accounts |
| `models` | Live catalog (`--filter free\|paid\|local`) |
| `status` | Bind / config path / version |
| `config` | `path` / `show` / `validate` / `init` |
| `clients` | Harness presets (`list` / `show` / `verify [--chat]`) |

## HTTP

| Path | Status |
|---|---|
| `GET /v1/models` | Live list; hide/expose affect **listing only** |
| `GET /v0/catalog` | Rich catalog (tier, modalities, privacy, hidden/routable) |
| `POST /v1/chat/completions` | Stream + non-stream; failover on 429/401 |
| `POST /v1/messages` | Native Anthropic SSE or translated OpenAI stream (true events, not a single-event wrapper) |
| `GET /` | UI: Accounts, Catalog, Showcase, Clients, Health, Settings |
| `GET /healthz` | Liveness (includes LAN warning flags; no admin token) |
| `GET /admin/health` | Bind, adapters, **account cooldowns** (token required off loopback) |
| `GET /admin/presets` | Account dropdown templates |
| `GET /admin/usage` | Persisted usage (`usage.json`) |

## Gemini

Google AI Studio is the **official OpenAI-compat Gemini API** (`https://generativelanguage.googleapis.com/v1beta/openai`), not `generateContent`. Adapter ids: `google` and alias `gemini`. Docs: [PROVIDERS.md](docs/PROVIDERS.md).

## What shipped in v0.2.0

- [x] Live catalog, OpenAI + Claude chat (incl. true SSE), vision Showcase
- [x] Native Anthropic / OpenAI / OpenRouter / OpenCode Zen adapters
- [x] Hosted presets: LM Studio, Groq, Cerebras, Google AI Studio, xAI, Hugging Face
- [x] Multi-account 429/401 failover + persisted usage
- [x] Clients: Cursor, Claude Code, OpenCode, Pi (both wires), Codex, Continue, Cline + `clients verify`
- [x] First-run default config on `serve`; example YAML; `PEAPROXY_*` env overlays
- [x] Refuse `0.0.0.0` without `--allow-lan` + admin token; UI LAN warning
- [x] Subscription OAuth: Claude Pro/Max, ChatGPT/Codex, Gemini/Antigravity, xAI, Kimi, Meta Muse (ToS/ban risk documented; API keys remain official) — [OAUTH.md](docs/OAUTH.md)
- [x] Tag **`v0.2.0`** on `main` (GoReleaser publishes binaries)

Post-v0.2 polish in this tree: OS keychain / encrypted-file secrets; Claude token 403 errors point at docs and the official API-key path. Qwen OAuth stays **not yet** (no CPA consumer flow).

## Why this exists

VibeProxy and CLIProxyAPI spend a lot of issue tracker time on:

1. **Auto model discovery** — stop the “add model X” treadmill.
2. **Failover that works** — quota/429 → next credential without hand-disabling accounts.
3. **Harness fidelity** — Pi cloak defaults, thinking injection, Cursor tools, Codex quirks.
4. **Secure localhost default** — `127.0.0.1:8317`, not `*:8317`.
5. **UI without a macOS tray** — CLI + browser only.
6. **Catalog hide ≠ routing** — listing-only exclusion (CPA #5995 still open).
7. **Prompt-cache-safe JSON** — never reshuffle keys with Go maps (VibeProxy #292).
8. **Free + custom providers** — including OpenCode Zen (CPA declined #6018).
9. **Built-in usage / showcase** — CPA removed usage in v6.10+.

Details: [docs/PLAN.md](docs/PLAN.md), [docs/COMPETITOR-WINS.md](docs/COMPETITOR-WINS.md), [docs/PROVIDERS.md](docs/PROVIDERS.md), [docs/HARNESS.md](docs/HARNESS.md), [docs/CONFIG.md](docs/CONFIG.md), [docs/OAUTH.md](docs/OAUTH.md).

## Security

- Default bind is **loopback**. Binding `0.0.0.0` requires `--allow-lan` **and** a non-empty admin token (`docs/CONFIG.md`).
- Never log secrets. Opt-in request log is redacted. OAuth tokens and inline API keys are stored in the OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service) or an AES-GCM file next to the config when no keychain is available. YAML lists accounts without printing those secrets.
- **ToS:** **Subscription OAuth** (Claude Pro/Max, ChatGPT/Codex, Gemini/Antigravity, xAI, Kimi, Meta Muse) may violate a provider’s terms and can result in account bans. PeaProxy authors are **not liable**. Prefer official API keys. OpenCode Zen **free** models may train on prompts — see catalog privacy notes and [OpenCode Zen docs](https://opencode.ai/docs/zen/). Details: [docs/OAUTH.md](docs/OAUTH.md).
- **GitHub Models is retired** (2026-07-30) and is not a provider.

## License

[MIT](LICENSE)
