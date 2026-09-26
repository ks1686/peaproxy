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
| `PEAPROXY_SECRET_BACKEND` | `file` forces the AES-GCM file next to the config; `keyring` requires the OS store (macOS Keychain / Windows Credential Manager / Linux Secret Service). Unset: try keyring, then file. `go test` always uses `file`. |

Prefer `apiKeyEnv` over inline `apiKey`. Inline keys and OAuth tokens are **not** written back to YAML; they go to the secret store. YAML still lists `providers[]` (id, adapter, email, expiry, non-secret extra).

Provider keys can also stay in their own env vars (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GROQ_API_KEY`, `CEREBRAS_API_KEY`, `GEMINI_API_KEY`, `XAI_API_KEY`, `HF_TOKEN`, `NVIDIA_API_KEY`, `CLOUDFLARE_API_TOKEN`, `OLLAMA_API_KEY`, `OPENROUTER_API_KEY`, `OPENCODE_API_KEY`) referenced as `apiKeyEnv` in YAML.

`peaproxy auth login --provider anthropic|openai|gemini|xai|kimi|meta` writes the account row plus secrets. See [OAUTH.md](OAUTH.md) for ToS/ban-risk liability. Never commit `config.yaml`, `secret.key`, or `secrets.enc`.

## LAN bind

Loopback (`127.0.0.1`, `localhost`, `::1`) needs no extra flags.

Binding `0.0.0.0` (or any non-loopback address) is refused unless **both**:

1. `--allow-lan` or `allowNonLoopback: true` or `PEAPROXY_ALLOW_LAN=1`
2. a non-empty `adminToken` (`--admin-token` / `PEAPROXY_ADMIN_TOKEN` / YAML)

Then `/admin/*` requires `X-Admin-Token` (or `Authorization: Bearer …`). `GET /healthz`, `/v1/*`, and the UI static files stay reachable; the UI shows a LAN warning and stores the token in `localStorage` for admin fetches.

## Catalog overlays

Live `ListModels` remains the source of IDs. Optional pin/rename live next to hide/expose:

```yaml
catalog:
  pin:
    - llama3.2
  rename:
    llama3.2: Llama 3.2 local
```

The UI Catalog page and `POST /admin/catalog/overlay` write the same fields. Hide still affects listing only (CPA #5995). Rename never becomes a routing alias — clients must use the live provider id.

## Request log

`requestLog: true` (or `PEAPROXY_REQUEST_LOG=1`, or the Request log / Settings toggle) appends redacted JSONL to `requests.log` next to the config. File mode is `0600`. The log rotates when it exceeds 1MiB. Bearer tokens, API keys, JWTs, and PEM private keys are stripped before write. `GET /admin/requests` tails the inspector for the UI. Usage counters still go to `usage.json` even when the inspector is off.
