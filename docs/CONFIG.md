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
| `PEAPROXY_CLIENT_ROOT` | Directory for managed harness connect/disconnect. Unset uses the home directory. Smoke tests set a temporary directory. |

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
- Pin/rename never become routing aliases — clients must use the live provider id, or a name from `routes`.
- Hidden pinned models stay off `/v1/models` and remain routable by id unless `hide.blockRouting` is true.

## Stable route names

```yaml
routes:
  code: llama3.2
```

A client that sends `model: code` is routed like `llama3.2`, and the upstream body is rewritten to that live id. The response `model` field is written back to `code`. A suffix `-thinking-4000` on the requested name sets an Anthropic thinking budget and raises `max_tokens` above it. The suffix is not a client-preset cloak. `POST /v1/images/edits` is proxied for the same `image_out` accounts as generations. A 502 or 504 is tried once more on that account before it is cooled. A 403 is returned as-is. `/v1/models` lists `code` while the target is in the live catalog. If the target is missing, the request fails and no upstream is called. A route name wins over a live model id with the same string. Names must be non-empty, without spaces, and different from their target. When `expose.models` is a non-empty subset, the route name has to be in that list to appear in `/v1/models`; it still routes if the client sends it.

The UI Catalog page, `POST /admin/catalog/overlay`, and `peaproxy catalog pin|rename|hide` write the same fields. Settings shows pin/rename/hide counts (edit them on Catalog).

## Failover

When several accounts list the same model id, PeaProxy tries them in policy order. Cooled accounts are omitted from that order.

```yaml
failover:
  policy: round-robin   # default
  # policy: fill-first
  # policy: sticky
  # sessionAffinity: false   # default is on
  # sessionAffinityTTL: 1h
```

| Policy | Behavior |
|---|---|
| `round-robin` | Rotate the starting hot account on each new conversation (default). |
| `fill-first` | Always start at the first hot account in YAML `providers` order. |
| `sticky` | Remember the last successful account per model and try it first; if it is cooled or fails, try the remaining hot accounts in YAML order and stick to whoever succeeds. |
| `adaptive` | Prefer measured accounts with lower in-flight work, then fewer errors, then lower latency. Accounts with no measurements stay in YAML order after the measured ones. |

Session affinity is separate from `sticky`. It keeps one conversation on the account that first succeeded, for `sessionAffinityTTL` (default 1 hour). The id comes from `X-Session-ID`, `X-Client-Request-Id`, `Session-Id`, `session_id`, `conversation_id`, a Claude metadata user id that already names a session, or a hash of the system prompt plus the first user turn. Later turns in that chat do not change the id. If the bound account is cooled, the next hot account takes the conversation. Set `sessionAffinity: false` to keep plain round-robin for identical chats. An empty `messages` list has no session, so those requests still rotate.

Retryable failures are HTTP **429**, **401**, **503**, **529**, plus provider error bodies that look like rate-limit / quota, overloaded, or auth-expired. Cooldown reasons are those classes (`rate-limit`, `overloaded`, `auth-expired`) — not raw bodies (no secrets). Plain `400 invalid_request_error` does not fail over.

A cooldown lasts 30s, or as long as the upstream's reset hint when it sends one: a `Retry-After` header, or for Antigravity / Cloud Code the error body (`quotaResetDelay`, `RetryInfo.retryDelay`, or "Resets in X"). Hints are clamped to 1s–1h. A transport failure (502, 504, or a 503 whose body is an edge proxy's connect error / reset before headers) is retried once on the same account and then skips it for only 5s, since it says nothing about the account. When every matching account is cooling, the 503 body reads `all matching accounts in cooldown: <account> after HTTP 429 (rate-limit), Ns left`. The `Retry-After` returned to clients is capped at 60s, because some clients (OpenCode) wait it out uncapped; the internal cooldown still keeps the full hint.

`peaproxy config validate` prints the effective `failover.policy`. Health UI and `peaproxy health` still list active cooldowns with remaining time, plus quota remaining when a provider reports it.

## Request engine

`requestEngine` and `automaticRoutes` are optional. Omitted, they keep the schema-1 path: three attempts, a two-minute deadline, prompt-cache bytes left as the caller sent them, no response cache, and no `pea/*` route.

```yaml
requestEngine:
  maxAttempts: 3
  deadline: 2m
  preludeTimeout: 30s
  promptCache: preserve # preserve | optimize | off
  cacheResponses: false
  cacheEmbeddings: false
  maxInFlight: 0 # 0 is unlimited
automaticRoutes:
  enabled: false
```

`preludeTimeout` (default 30s; 5s before v2.0.7) is how long a stream may wait for its first event. When it expires, the request moves to the next matching account only when another one can take the request. It never retries the same account and never starts a cooldown. The last account a request can reach is never cut off by it; only `deadline` bounds that attempt.

`promptCache: optimize` adds one Anthropic `cache_control` breakpoint only for a known profile and only when the caller is under that profile's limit. `off` does not strip caller breakpoints. Response caching is exact, in-memory, and skips tools, images, and continuation ids. `pea/auto`, `pea/economy`, `pea/local`, and `pea/free` are rejected as `routes` names. They select a live model only when `automaticRoutes.enabled` is true. Unknown prices are not free and do not win economy. An exact local model does not fail over to a cloud account that happens to advertise the same id.

## Request log

`requestLog: true` (or `PEAPROXY_REQUEST_LOG=1`, or the Request log **or** Settings toggle — they share `POST /admin/settings`) appends redacted JSONL to `requests.log` next to the config. File mode is `0600`. The log rotates when it exceeds 1MiB. Bearer tokens, API keys, JWTs, and PEM private keys are stripped before write. If that response included rate-limit remaining headers, the row carries a compact `quotaHint` (honest 0 is shown; unknown remaining is omitted). `GET /admin/requests` and `peaproxy requests tail` read that inspector. Usage counters still go to `usage.json` even when the inspector is off. `peaproxy health` prints the same bind / adapterHealth / quota / cooldowns fields as `GET /admin/health`; cooldown lines include last-known remaining when the provider reported it.

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
