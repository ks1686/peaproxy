# PeaProxy

Local multi-provider AI gateway in Go: **API keys + free/local providers** (subscription OAuth is stubbed), first-class text + images, a **live auto model catalog** (no hand-maintained allowlist), OpenAI- and Claude-compatible endpoints, a CLI service, and a localhost UI.

Status: **usable product core**. Add Ollama, an Anthropic/OpenAI/OpenRouter key, or OpenCode Zen — live models, chat, Claude SSE, vision in Showcase, multi-account failover. **OAuth login is not implemented.**

## One-liner

Maximize whatever you already pay for (OAuth later, where we can do it cleanly), fall back to keys, and treat Ollama / LM Studio / OpenRouter-free / OpenCode Zen as first-class — then expose one OpenAI-shaped and one Claude-shaped local endpoint that coding tools already speak.

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

1. **Accounts** — pick a preset (Ollama, Anthropic API key, OpenAI API key, OpenRouter, OpenCode Zen, or custom OpenAI-compat).
2. **Catalog** — live `ListModels`. Hide is listing-only (CPA #5995).
3. **Showcase** — try a prompt; vision models accept an image URL or upload.
4. Point Cursor / OpenCode / Claude Code at the local base URL (`peaproxy clients show …`).

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
```

Config defaults to `~/.config/peaproxy/config.yaml` (created when you add accounts). Example: [configs/peaproxy.example.yaml](configs/peaproxy.example.yaml). Usage is persisted as `usage.json` next to the config; set `requestLog: true` for a redacted `requests.log`.

| Command | Purpose |
|---|---|
| `serve` | Listen `127.0.0.1:8317` + UI |
| `--version` | Build version (`dev` unless a release ldflag) |
| `auth` | OAuth login **stub** |
| `accounts` | Configured provider accounts |
| `models` | Live catalog (`--filter free\|paid\|local`) |
| `status` | Bind / config path / version |
| `config` | `path` / `show` / `validate` |
| `clients` | Harness presets (`list` / `show` / `verify`) |

## HTTP

| Path | Status |
|---|---|
| `GET /v1/models` | Live list; hide/expose affect **listing only** |
| `GET /v0/catalog` | Rich catalog (tier, modalities, privacy, hidden/routable) |
| `POST /v1/chat/completions` | Stream + non-stream; failover on 429/401 |
| `POST /v1/messages` | Native Anthropic SSE or translated OpenAI stream (true events, not a single-event wrapper) |
| `GET /` | UI: Accounts, Catalog, Showcase, Clients, Health, Settings |
| `GET /admin/health` | Bind, adapters, **account cooldowns** |
| `GET /admin/usage` | Persisted usage (`usage.json`) |

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

Details: [docs/PLAN.md](docs/PLAN.md), [docs/COMPETITOR-WINS.md](docs/COMPETITOR-WINS.md), [docs/PROVIDERS.md](docs/PROVIDERS.md), [docs/HARNESS.md](docs/HARNESS.md).

## Security

- Default bind is **loopback**. Binding `0.0.0.0` requires `allowNonLoopback: true` **and** a non-empty `adminToken`.
- Never log secrets. Opt-in request log is redacted.
- **ToS:** subscription OAuth through a local proxy may violate a provider’s terms. OpenCode Zen **free** models may train on prompts — see catalog privacy notes and [OpenCode Zen docs](https://opencode.ai/docs/zen/).
- **GitHub Models is retired** (2026-07-30) and is not a provider.

## License

[MIT](LICENSE)
