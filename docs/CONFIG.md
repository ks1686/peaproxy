# Config

PeaProxy reads a versioned YAML file, then overlays environment variables, then CLI flags.

**Order:** config file &lt; `PEAPROXY_*` env &lt; flags (`--bind`, `--port`, `--allow-lan`, `--admin-token`).

## Path

Default: `~/.config/peaproxy/config.yaml` (or `%AppData%\peaproxy\config.yaml` on Windows).

Override with `--config /path/to/peaproxy.yaml`.

`peaproxy config path` prints the resolved path. `peaproxy config init` writes the default skeleton if missing. **`peaproxy serve` also writes that skeleton on first run** (it prints `wrote first-run config …`).

Example checked into the repo: [configs/peaproxy.example.yaml](../configs/peaproxy.example.yaml). File mode is `0600`.

## Environment

| Variable | Effect |
|---|---|
| `PEAPROXY_BIND` | Listen host (`127.0.0.1` default) |
| `PEAPROXY_PORT` | Listen port (`8317` default) |
| `PEAPROXY_ADMIN_TOKEN` | Admin token for `/admin` when bound off loopback |
| `PEAPROXY_ALLOW_LAN` | `1` / `true` / `yes` / `on` sets `allowNonLoopback` |
| `PEAPROXY_REQUEST_LOG` | same truthy values enable redacted `requests.log` |

Provider keys stay in their own env vars (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GROQ_API_KEY`, `CEREBRAS_API_KEY`, `GEMINI_API_KEY`, `XAI_API_KEY`, `HF_TOKEN`, `OPENROUTER_API_KEY`, `OPENCODE_API_KEY`) referenced as `apiKeyEnv` in YAML.

## LAN bind

Loopback (`127.0.0.1`, `localhost`, `::1`) needs no extra flags.

Binding `0.0.0.0` (or any non-loopback address) is refused unless **both**:

1. `--allow-lan` or `allowNonLoopback: true` or `PEAPROXY_ALLOW_LAN=1`
2. a non-empty `adminToken` (`--admin-token` / `PEAPROXY_ADMIN_TOKEN` / YAML)

Then `/admin/*` requires `X-Admin-Token` (or `Authorization: Bearer …`). `GET /healthz`, `/v1/*`, and the UI static files stay reachable; the UI shows a LAN warning and stores the token in `localStorage` for admin fetches.
