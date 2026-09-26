# PeaProxy

Local multi-provider AI gateway in Go: **subscription OAuth + API keys + free/local providers**, first-class text + images, a **live auto model catalog** (no hand-maintained allowlist), OpenAI- and Claude-compatible endpoints, a CLI service, and a localhost UI.

Status: **usable v0.1 core**. Add Ollama or an OpenRouter key, see live models, chat from Showcase or `curl`. **OAuth login is not implemented.**

## One-liner

Maximize whatever you already pay for (OAuth where we can do it cleanly), fall back to keys, and treat Ollama / LM Studio / OpenRouter-free / OpenCode Zen as first-class — then expose one OpenAI-shaped and one Claude-shaped local endpoint that coding tools already speak.

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

## Quick start

```bash
go run ./cmd/peaproxy serve
```

Open http://127.0.0.1:8317/ → **Accounts** → add Ollama (or OpenRouter with a key) → **Catalog** → **Showcase**.

```bash
curl -s http://127.0.0.1:8317/v1/models
curl -s http://127.0.0.1:8317/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"llama3.2","messages":[{"role":"user","content":"hi"}]}'
peaproxy clients show cursor
peaproxy clients show opencode   # includes /v1
peaproxy clients show claude-code  # does NOT include /v1
```

Config defaults to `~/.config/peaproxy/config.yaml` (created when you add accounts). Example: [configs/peaproxy.example.yaml](configs/peaproxy.example.yaml).

| Command | Purpose |
|---|---|
| `serve` | Listen `127.0.0.1:8317` + UI |
| `auth` | OAuth login **stub** |
| `accounts` | Configured provider accounts |
| `models` | Live catalog (`--filter free\|paid\|local`) |
| `status` | Bind / config path |
| `config` | `path` / `show` / `validate` |
| `clients` | Harness presets (`list` / `show` / `verify`) |

## HTTP

| Path | Status |
|---|---|
| `GET /v1/models` | Live list; hide/expose affect **listing only** |
| `GET /v0/catalog` | Rich catalog (tier, privacy, hidden/routable flags) |
| `POST /v1/chat/completions` | Stream + non-stream via adapters |
| `POST /v1/messages` | Claude → OpenAI translation for ollama/openai_compat |
| `GET /` | UI: Accounts, Catalog, Showcase, Clients, Health, Settings |
| `GET /admin/usage` | In-memory usage log |

## Security

- Default bind is **loopback**. Binding `0.0.0.0` requires `allowNonLoopback: true` **and** a non-empty `adminToken`.
- Never log secrets.
- **ToS:** subscription OAuth through a local proxy may violate a provider’s terms. OpenCode Zen **free** models may train on prompts — see catalog privacy notes.
- **GitHub Models is retired** (2026-07-30) and is not a provider.

## License

[MIT](LICENSE)
