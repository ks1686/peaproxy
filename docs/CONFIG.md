# Config

PeaProxy reads a versioned YAML file, then overlays environment variables, then CLI flags.

Providers and auth paths: [PROVIDERS.md](PROVIDERS.md), [OAUTH.md](OAUTH.md). 1.0 residuals: [V1.md](V1.md).

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
| `PEAPROXY_FAILOVER_POLICY` | `round-robin` (default), `fill-first`, or `sticky` |
| `PEAPROXY_SECRET_BACKEND` | `file` forces the AES-GCM file next to the config; `keyring` requires the OS store (macOS Keychain / Windows Credential Manager / Linux Secret Service). Unset: try keyring, then file. `go test` always uses `file`. |

Prefer `apiKeyEnv` over inline `apiKey`. Inline keys and OAuth tokens are **not** written back to YAML; they go to the secret store. YAML still lists `providers[]` (id, adapter, email, expiry, non-secret extra).

Provider keys can also stay in their own env vars (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GROQ_API_KEY`, `CEREBRAS_API_KEY`, `GEMINI_API_KEY`, `XAI_API_KEY`, `HF_TOKEN`, `NVIDIA_API_KEY`, `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`, `OLLAMA_API_KEY`, `SAMBANOVA_API_KEY`, `OPENROUTER_API_KEY`, `OPENCODE_API_KEY`) referenced as `apiKeyEnv` in YAML. `CLOUDFLARE_ACCOUNT_ID` is **not** a secret key: it fills `YOUR_ACCOUNT_ID` in the Workers AI base URL (`peaproxy accounts add workers-ai` does the same as the Accounts UI). The Accounts UI names these env vars and whether they are set; it never prints values.

`peaproxy auth login --provider anthropic|openai|gemini|xai|kimi|meta|copilot` writes the account row plus secrets. See [OAUTH.md](OAUTH.md) for ToS/ban-risk liability. Never commit `config.yaml`, `secret.key`, or `secrets.enc`.

## LAN bind

Loopback (`127.0.0.1`, `localhost`, `::1`) needs no extra flags.

Binding `0.0.0.0` (or any non-loopback address) is refused unless **both**:

1. `--allow-lan` or `allowNonLoopback: true` or `PEAPROXY_ALLOW_LAN=1`
2. a non-empty `adminToken` (`--admin-token` / `PEAPROXY_ADMIN_TOKEN` / YAML)

Then `/admin/*` requires `X-Admin-Token` (or `Authorization: Bearer …`). `GET /healthz`, `/v1/*`, and the UI static files stay reachable; the UI shows a LAN warning and stores the token in `localStorage` for admin fetches. Settings → Listen repeats the bind/LAN warning and whether an admin token is required.

## Catalog overlays

Live `ListModels` remains the source of IDs. Optional pin/rename live next to hide/expose:

```yaml
catalog:
  pin:
    - llama3.2
  rename:
    llama3.2: Llama 3.2 local
```

Rules (`peaproxy config validate`):

- Pin ids must be non-empty and unique.
- Rename keys (live model ids) and display names must be non-empty.
- Hide/expose lists must not contain empty ids or duplicates.
- Pin/rename never become routing aliases — clients must use the live provider id.
- Hidden pinned models stay off `/v1/models` and remain routable by id unless `hide.blockRouting` is true.

The UI Catalog page, `POST /admin/catalog/overlay`, and `peaproxy catalog pin|rename|hide` write the same fields. Settings shows pin/rename/hide counts (edit them on Catalog).

## Failover

When several accounts list the same model id, PeaProxy tries them in policy order. Cooled accounts (30s skip after a retryable failure) are omitted from that order.

```yaml
failover:
  policy: round-robin   # default
  # policy: fill-first
  # policy: sticky
```

| Policy | Behavior |
|---|---|
| `round-robin` | Rotate the starting hot account on each request (default). |
| `fill-first` | Always start at the first hot account in YAML `providers` order. |
| `sticky` | Remember the last successful account per model and try it first; if it is cooled or fails, try the remaining hot accounts in YAML order and stick to whoever succeeds. |

Retryable failures are HTTP **429**, **401**, **503**, **529**, plus provider error bodies that look like rate-limit / quota, overloaded, or auth-expired. Cooldown reasons are those classes (`rate-limit`, `overloaded`, `auth-expired`) — not raw bodies (no secrets). Plain `400 invalid_request_error` does not fail over.

`peaproxy config validate` prints the effective `failover.policy`. Health UI and `peaproxy health` still list active cooldowns with remaining time, plus quota remaining when a provider reports it.

## Request log

`requestLog: true` (or `PEAPROXY_REQUEST_LOG=1`, or the Request log **or** Settings toggle — they share `POST /admin/settings`) appends redacted JSONL to `requests.log` next to the config. File mode is `0600`. The log rotates when it exceeds 1MiB. Bearer tokens, API keys, JWTs, and PEM private keys are stripped before write. `GET /admin/requests` and `peaproxy requests tail` read that inspector. Usage counters still go to `usage.json` even when the inspector is off. `peaproxy health` prints the same bind / adapterHealth / quota / cooldowns fields as `GET /admin/health`.

## Validate

```bash
peaproxy config validate
peaproxy config validate --config ./peaproxy.yaml
```

Exits non-zero when the file is invalid. Prints a summary on success:

```
ok
path: /home/you/.config/peaproxy/config.yaml
bind: 127.0.0.1:8317
loopback: true
requestLog: false
catalog.pin: 0
catalog.rename: 0
failover.policy: round-robin
providers: 1
secrets: file
```

`secrets` is `file` or `keyring` — never token values. Unknown adapter names fail validate (see [PROVIDERS.md](PROVIDERS.md)). Invalid `tier` values (not `free|freemium|paid|local`) fail. First-run `serve` writes the skeleton, then the same checks apply.

## Settings UI

`GET /admin/settings` (and the Settings page) reports bind/port, loopback vs LAN warning, config path, secret-backend name + note, request-log on/off and path, and catalog overlay counts. It never returns `adminToken`, API keys, or OAuth tokens. `hasAdminToken` is a boolean only.
