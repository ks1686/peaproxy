# PeaProxy

Local multi-provider AI gateway in Go: **subscription OAuth + API keys + free/local providers**, first-class text + images, a **live auto model catalog** (no hand-maintained allowlist), OpenAI- and Claude-compatible endpoints, a CLI service, and a localhost UI.

Status: **v0 scaffold**. Adapters compile and the HTTP surface exists. **OAuth login is not implemented.** Do not treat this tree as a working Claude/ChatGPT subscription proxy yet.

## One-liner

Maximize whatever you already pay for (OAuth where we can do it cleanly), fall back to keys, and treat Ollama / LM Studio / OpenRouter-free as first-class — then expose one OpenAI-shaped and one Claude-shaped local endpoint that coding tools already speak.

## Why this exists

VibeProxy and CLIProxyAPI spend a lot of issue tracker time on:

1. **Auto model discovery** — stop the “add model X” treadmill.
2. **Failover that works** — quota/429 → next credential without hand-disabling accounts.
3. **Harness fidelity** — Pi cloak defaults, thinking injection, Cursor tools, Codex quirks.
4. **Secure localhost default** — `127.0.0.1:8317`, not `*:8317`.
5. **UI without a macOS tray** — CLI + browser only (no TCC/menu-bar fight).
6. **Credential reliability** — refresh must not wipe the account list.
7. **Onboarding** — copy-paste presets for Cursor, OpenCode, Pi, Claude Code, Codex.
8. **Free + custom providers** — first-class, with Free/Paid/Local catalog filters.
9. **Multimodal** — vision in v1; image-out as a live capability flag.

PeaProxy is built around those wins. Details: [docs/PLAN.md](docs/PLAN.md), [docs/COMPETITOR-WINS.md](docs/COMPETITOR-WINS.md), [docs/PROVIDERS.md](docs/PROVIDERS.md), [docs/HARNESS.md](docs/HARNESS.md).

## Quick start (scaffold)

```bash
go run ./cmd/peaproxy --help
go run ./cmd/peaproxy status
go run ./cmd/peaproxy serve
```

Then open http://127.0.0.1:8317/

| Command | Purpose |
|---|---|
| `serve` | Listen (default `127.0.0.1:8317`) + localhost UI |
| `auth` | OAuth login **stub** |
| `accounts` | Configured provider accounts |
| `models` | Catalog list stub (`GET /v1/models` is the real path) |
| `status` | Bind / phase |
| `config` | `path` / `show` / `validate` |
| `clients` | Harness presets (`list` / `show` / `verify`) |

```bash
peaproxy clients show cursor
peaproxy models list --filter free
```

Copy [configs/peaproxy.example.yaml](configs/peaproxy.example.yaml) and pass `--config`.

## HTTP (placeholders)

| Path | Status |
|---|---|
| `GET /v1/models` | Live-catalog **shape** + hide/filter (in-memory / config) |
| `POST /v1/chat/completions` | Routed to adapters when configured; otherwise stub |
| `POST /v1/messages` | Claude-compatible **stub** |
| `GET /` | Localhost UI (Accounts, Catalog, Showcase, Clients, Health, Settings) |
| `GET /admin/health` | Health JSON |

## Security

- Default bind is **loopback**. Binding `0.0.0.0` requires `allowNonLoopback: true` **and** a non-empty `adminToken`.
- Never log secrets. Request inspector (later) redacts by default.
- **ToS:** routing subscription OAuth through a local proxy may violate a provider’s terms. You run that risk; PeaProxy will not hide it.

## License

[MIT](LICENSE)

Pea Pod OSS — a pea-pod.me/peaproxy page comes only when this is actually shippable.
