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
| `PEAPROXY_DEBUG` | same truthy values log the Claude OAuth tool alias mapping (`client -> upstream`) on a 4xx, so a `400 tool name ...` can be traced to the name the client actually used. Account id, status and pairs only — no headers, body or tokens. |
| `PEAPROXY_FAILOVER_POLICY` | `round-robin` (default), `fill-first`, or `sticky` |
| `PEAPROXY_SECRET_BACKEND` | `file` forces the AES-GCM file next to the config; `keyring` requires the OS store (macOS Keychain / Windows Credential Manager / Linux Secret Service). Unset: try keyring, then file. `go test` always uses `file`. |
| `PEAPROXY_CLIENT_ROOT` | Directory for managed harness connect/disconnect. Unset uses the home directory. Smoke tests set a temporary directory. |

Prefer `apiKeyEnv` over inline `apiKey`. Inline keys and OAuth tokens are **not** written back to YAML; they go to the secret store. YAML still lists `providers[]` (id, adapter, email, expiry, non-secret extra).

Provider keys can also stay in their own env vars (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GROQ_API_KEY`, `CEREBRAS_API_KEY`, `GEMINI_API_KEY`, `XAI_API_KEY`, `HF_TOKEN`, `NVIDIA_API_KEY`, `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`, `OLLAMA_API_KEY`, `SAMBANOVA_API_KEY`, `OPENROUTER_API_KEY`, `OPENCODE_API_KEY`) referenced as `apiKeyEnv` in YAML. `CLOUDFLARE_ACCOUNT_ID` is **not** a secret key: it fills `YOUR_ACCOUNT_ID` in the Workers AI base URL (`peaproxy accounts add workers-ai` does the same as the Accounts UI). The Accounts UI names these env vars and whether they are set; it never prints values.

`peaproxy auth login --provider anthropic|openai|gemini|xai|kimi|meta|copilot` writes the account row plus secrets. See [OAUTH.md](OAUTH.md) for ToS/ban-risk liability. Never commit `config.yaml`, `secret.key`, or `secrets.enc`. `config.lock` and `secrets.lock` in the same directory are empty lock files that PeaProxy processes take while writing (up to 15 s wait, then a `busy` error); leave them in place.

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
- `/admin/catalog` reports a `contextWindow` for models whose provider publishes one. PeaProxy does not guess a value from a name table — a number it invented would be wrong for every model released after the table was written — so a model whose provider stays silent shows a dash. A context window is never set by hand.
- A model the provider refuses to serve on the chat wire (`ModelProtocolUnsupported`, "Model does not support this protocol") is dropped from `/v1/models` once an account has actually answered that way, and is kept in `/admin/catalog` with its reason. An earlier refusal is short-circuited locally, so the model stops being offered without a second upstream call. Un-hide does not force it back into the client list.

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

An **entitlement** failure (HTTP 402, exhausted free tier, spent quota) is not transient and gets its own **15 minute** cooldown rather than 30s. It does not clear in half a minute, so the short cooldown meant re-testing an account already known to be unusable, repeatedly. A `Retry-After` still outranks this: the provider is telling us when to return.

A cooldown otherwise lasts 30s, or as long as the upstream's reset hint when it sends one: a `Retry-After` header, or for Antigravity / Cloud Code the error body (`quotaResetDelay`, `RetryInfo.retryDelay`, or "Resets in X"). Hints are clamped to 1s–1h, except Cloud Code body hints, which are honoured up to 7 days because its weekly quota says "Resets in 166h…" and the cooldown is per model. A transport failure (502, 504, or a 503 whose body is an edge proxy's connect error / reset before headers) is retried once on the same account and then skips it for only 5s, since it says nothing about the account. When every matching account is cooling, the 503 body reads `all matching accounts in cooldown: <account> after HTTP 429 (rate-limit), Ns left`. The `Retry-After` returned to clients is capped at 60s, because some clients (OpenCode) wait it out uncapped; the internal cooldown still keeps the full hint.

`peaproxy config validate` prints the effective `failover.policy`. Health UI and `peaproxy health` still list active cooldowns with remaining time, plus quota remaining when a provider reports it.

## Optimization (v3)

`optimization` is the v3 cost block. It is **additive**: a config without it keeps working, and every field defaults to the opinionated v3 behaviour. A field you set explicitly always wins over the default.

```yaml
optimization:
  policyVersion: 1 # checked, not guessed at
  automatic: true # unset means true
  promptCache: optimize # preserve | optimize | off
  contextOptimization: true
  freeOnly: false
  allowAnonymousProviders: false
  spendCeilingUSD: 0 # 0 means no ceiling configured
  localAssistant: false
  persistentContext: false
  localAssistantEndpoint:
    endpoint: http://127.0.0.1:11234/v1
    model: "" # empty uses the endpoint's first model
    timeoutSeconds: 10
```

Booleans are nullable internally so that "you did not say" stays distinct from "you said no". That distinction is the difference between a default PeaProxy can improve on and one it must respect.

`policyVersion` is validated. A file written by a newer PeaProxy is **refused** with a clear message rather than silently misread.

`automatic: false` restores pre-v3 optimization behaviour in one setting and is the documented rollback.

`requestEngine.promptCache` still wins over `optimization.promptCache`, because it is a decision made before the v3 block existed.

### Money guards

`freeOnly: true` refuses any deployment that cannot **prove** the call will not be billed. The refusal names the setting, because a silent refusal looks like an outage. A deployment whose provider publishes a non-zero cache rate is not free, however cheap its input and output are; a provider that publishes no cache rate is not treated as charging for one. Local deployments are exempt, because they cost the user nothing.

`spendCeilingUSD` caps spend in a rolling window and **fails closed**. If recorded spend cannot be measured, PeaProxy refuses rather than proceeding — the accounts whose prices are least known are exactly where guessing wrong costs money. The policy panel at `GET /admin/policy` shows the resolved values; an unset ceiling reports `null`, never `0`, and says whether the recorded figure is a sum or a floor.

Both guards apply on every route, including a model the client names outright. Naming a model is not a way around the switch that protects the account.

### Anonymous providers, and what counts as an account

`allowAnonymousProviders` gates **automatic routes only** — the ones that pick the deployment for you. A `pea/*` route or a model you named outright goes where you pointed it, because that choice was yours to make.

The default (`false`, and `false` when the field is absent) refuses a deployment with **no credential of any kind**: no `apiKey`, no `apiKeyEnv`, and no configured OAuth session. Sending a prompt to such an endpoint publishes it to whoever runs that machine, and the refusal names the setting and the two ways out — attach an account, or say you meant it.

Two cases are deliberately not treated as anonymous. A **local** account has no key and never needed one; it is your own machine. An **OAuth** session is an account just as much as a key — Copilot-, Claude- and Codex-hosted endpoints report no API key at all, and refusing them would have been a self-inflicted outage. A configured OAuth session whose token has expired also counts as an account: that failure is a login that needs refreshing, not an anonymous endpoint, and reporting it as the latter would send you looking in the wrong place.

### Correcting what an adapter assumes about your endpoint

An OpenAI-compatible server is a wire shape, not a promise. Plenty of them accept a `tools` array and quietly ignore it, and nothing observable from here says so. A **subscription session is an account**; per-provider `capabilities` is how you state what your machine actually does:

```yaml
providers:
  - id: my-llama
    adapter: openai_compat
    baseURL: http://127.0.0.1:11434/v1
    capabilities:
      tools: false
      embeddings: false
```

Every field is optional and unset means "keep the adapter's own declaration" — only `tools`, `visionIn`, `imageOut` and `embeddings` can be corrected, because those are the ones an adapter asserts without being able to check. A declared `false` is believed in both directions: routing will not send a tool call there, and `/v1/models` and the health report say `no` rather than `unknown`, because you already know and PeaProxy has no reason to pretend otherwise.

**An unknown key is reported, not refused.** The loader does not reject unknown
keys, so `capabilites:` never took effect — but `peaproxy config validate` and
`peaproxy serve` now name every key the file carries that this version does not
read, with the path that carries it and, where one key is recognisably a typo of
another, what to write instead:

```
warning: 1 config key(s) are not read by this version and are ignored:
  providers[0].baseUrl -- did you mean "baseURL"?
  ignored keys keep their default, so the setting stays whatever it was
```

Map entries are not field names and are never reported this way, so `routes:` and
`catalog.rename:` keys are left alone.

It warns rather than refuses because a config carrying an unknown key is a working
config today, and failing on it would break a running deployment over a spelling.
That is why refusing is still an open question rather than a decision here. Once
the configs in the wild are clean, the same list can become the error it should
have been from the start.

Confirm a correction took effect by watching the health report change
(`peaproxy health` lists the capabilities PeaProxy believes each account has).

**`config validate` reaches the same verdict as `serve`.** It used to print `ok`
for a config the gateway then refused to start on — a case-mistyped `baseUrl` is
the common one, since the endpoint silently stays empty. Validate now builds every
configured account's adapter with the same options the gateway uses, so the two
cannot drift, and reports the gateway's own error instead of approving the config.
The unknown-key warning is printed *before* that failure on purpose: the typo is
the diagnosis, and "baseURL is required" is only its symptom.

#### How the ledger is priced

A provider's own `usage.cost` is the bill and is recorded as-is. Most providers publish none, so PeaProxy prices those calls itself from the published token counts and the deployment's quote, and reports that figure separately as an estimate (`estimatedUSD` on an event, `estimatedLast30DaysUSD` in the policy panel). An estimate is recorded only when the quote covers every component the call touched:

- A call whose usage was published on one side only stays unmeasured. Half a call's tokens are still spend.
- A call that read or wrote the provider's cache on a quote with no cache rate stays unmeasured.
- OpenAI counts cached tokens inside the prompt total; they are charged once, at the cache rate. Anthropic counts them beside it and nothing is subtracted.

`automaticRoutes.prices` accepts optional `cacheRead` and `cacheWrite` rates. A configured quote without them can price calls that never touch the cache.

#### Reservations, and what a ceiling is not

Before a request goes upstream, PeaProxy holds what that request's input can cost — bounded by the request's byte length, since every token is at least one byte — so a burst of concurrent requests cannot all read the same total and all decide the request is affordable. The hold is taken against the ceiling in the same locked step as the reading, and released when the call finishes; the completed call's measured cost is what the ledger keeps.

What a ceiling is **not**: a hard cap on concurrent output. Output length is unknowable before the model answers, so output spend from requests that are in flight at the same time is reconciled after the fact rather than prevented. The overshoot is bounded by `requestEngine.maxInFlight`.

What a ceiling is also **not**: a cap on a deployment nobody has priced. A hold is computed from a published rate; without one there is nothing to hold, and inventing a figure would charge a ceiling for a number PeaProxy made up. A priced deployment refusing a large request while an unpriced one still accepts it is the consequence of that, and it persists until the deployment is priced. Routing still checks unpriced deployments against the ceiling before selecting them — they are never treated as free — so the gap is the concurrency burst, not the ordinary single request.

### Local assistant

Off unless both `localAssistant: true` and an endpoint are set. The endpoint **must** resolve to loopback; there is no setting that allows anything else, because a helper described as local that can be pointed at a remote host is not one. Setting an endpoint with `localAssistant: false` is a validation error rather than a silently ignored field.

### Cursor Agent

`adapter: cursor_agent` runs the `cursor-agent` CLI as an upstream. It costs your Cursor subscription, not tokens per million, so it is priced as a flat tier and needs no price quote.

Set `workDir` on the provider:

```yaml
providers:
  - id: cursor-sub
    adapter: cursor_agent
    tier: paid
    workDir: /Users/you/cursor-scratch
```

`cursor-agent` asks for **workspace trust** before it will read anything, and that prompt is interactive — a service cannot answer it. Naming a directory with `workDir` both pins where the agent runs and passes `--trust` for that directory: naming it is the operator stating which directory is trusted. Left unset, no `--trust` is passed and the agent inherits PeaProxy's own working directory, so a run started from an unvetted place is refused with a message naming `workDir` rather than silently granted access.

Two more limits worth knowing before routing to it. It runs one prompt per call, not a message array, so a conversation is flattened into a transcript with roles preserved. And it has no tool calling, so `tools` is reported unsupported rather than unknown, and a request that needs tools will not route here.

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

Economy ranks on both halves of a price. A deployment that is not worse on input or output wins outright; where the rates cross — cheap to prompt with, expensive to read from — they are compared on a 3:1 input:output blend, because ranking on the input rate alone picks the more expensive answer for every conversation that produces an answer worth reading.

`maxInFlight: 0` is unlimited. A non-zero value caps the requests in flight **per account across every path** — chat, the native Responses and Messages wires, both stream and non-stream, and image, edit and embedding calls. A request that finds its account at the cap is treated the way a cooled account is treated, with one difference: it moves to the next candidate and is **not** left as a failure when another account can take the request, and it never starts a cooldown. The cap is a load guard, not a health signal, so a busy account is never marked broken because of it. If every candidate is busy, the client gets a busy error rather than a `cooldown` error.

Image and edit calls are **not** failed over on a **502** or **504**, unlike every other path. A gateway that stopped waiting says nothing about whether the provider already generated (and billed) the image, so replaying risks paying twice; the client is told what happened instead. Embeddings stay idempotent and still fail over. A **500** was never replayed on any path.

## Request log

`requestLog: true` (or `PEAPROXY_REQUEST_LOG=1`, or the Request log **or** Settings toggle — they share `POST /admin/settings`) appends redacted JSONL to `requests.log` next to the config. File mode is `0600`. The log rotates when it exceeds 1MiB. Bearer tokens, API keys, JWTs, and PEM private keys are stripped before write. If that response included rate-limit remaining headers, the row carries a compact `quotaHint` (honest 0 is shown; unknown remaining is omitted). `GET /admin/requests` and `peaproxy requests tail` read that inspector. Usage counters still go to `usage.json` even when the inspector is off. `peaproxy health` prints the same bind / adapterHealth / quota / cooldowns fields as `GET /admin/health`; cooldown lines include last-known remaining when the provider reported it.

## Editing a running config

A running `peaproxy serve` watches `config.yaml` and adopts what it finds. An account added by `peaproxy auth login`, a `catalog`/`hide`/`routes` edit from the CLI, or a hand edit all take effect within about two seconds, with secrets hydrated from the secret store. A file that is invalid while you are mid-edit is logged once and left alone; the running config is not disturbed, and the next valid write is picked up.

**Adopted without a restart:** `providers`, `hide`, `expose`, `catalog`, `routes`, `failover`, `automaticRoutes`, `requestEngine`, `optimization` — everything read per request.

**Needs a restart:** `bind` and `port` (the listener is already bound), `allowNonLoopback` and `adminToken` (a security posture that should not change under live traffic by editing a file), `requestLog` (the log file handle is opened at start), and `schemaVersion`. The UI Settings page still applies these immediately, because that is an explicit action by someone looking at the screen.

The watcher polls the file's size and mtime every 2 s. It takes `saveMu` without blocking, so it never reads a file another writer is in the middle of replacing, and it ignores the signature of its own writes.

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
