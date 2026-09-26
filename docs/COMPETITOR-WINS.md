# Competitor issue wins → PeaProxy design goals

Sources: open/recent issues on automazeio/vibeproxy and router-for-me/CLIProxyAPI (sampled 2026-09-26). Originally **product goals**. Status below is **v0.2.6 on main**, not a claim that every upstream issue is closed.

Cross-links: [PLAN.md](PLAN.md) (phases), [PROVIDERS.md](PROVIDERS.md) (adapters), [HARNESS.md](HARNESS.md) (client presets), [OAUTH.md](OAUTH.md) (liability), [V1.md](V1.md) (1.0 cut).

## Themes PeaProxy must beat

### 1. Live model catalog (no hand maintenance) — shipped

CLIProxyAPI repeatedly gets "add model X" issues (Opus 4.6/4.7, Fable, GPT Daybreak, Gemini Flash variants, Devin SWE models missing from catalog #6111 regression). Hand-maintained registries go stale.

**PeaProxy:** adapters always list from provider live endpoints; never ship allowlists as source of truth; enrich optionally. Hide/pin/rename are overlays only.

### 2. Failover that actually fails over — shipped

#6135 failover broken when quota exhausted; #6130 scheduler can't terminal-reject; cooldown/429 storms (#1015, #903).

**PeaProxy:** `failover.policy` is `round-robin` (default), `fill-first`, or `sticky`. HTTP 429/401/503/529 **and** rate-limit / overloaded / auth-expired error bodies cool that account for 30s and try the next; cooled accounts are not re-hit (503 + `Retry-After`). Health UI and `peaproxy health` show remaining cooldown. Cooldown reasons are classes, not secret-bearing bodies.

### 3. Protocol fidelity for coding harnesses — shipped (documented limits)

- Claude cloak defaults break Pi / non-Claude-Code (#6120) — PeaProxy cloak defaults **off**
- Thinking/clear_thinking injection breaks plain Claude (#509 VibeProxy) — not injected
- Tool-result adjacency / translator bugs (#6129, #4112) — still a translation risk on non-native wires
- Cursor tool blocks on OpenAI wire (#411 VibeProxy)
- Codex Responses vs Messages quirks; image_gen tool conflicts (#456) — `POST /v1/responses` exists; image-gen tool is **not** a generations proxy
- Amp WebSocket / stream_options failures — Custom URL only; **no** Amp WebSocket; Codex OAuth drops `stream_options`

**PeaProxy:** harness profiles (Pi, OpenCode, Cursor, Claude Code, Codex, Continue, Cline, Amp) + `clients verify [--chat]`. Factory Droid is not a preset.

### 4. Security defaults — shipped

VibeProxy ThinkingProxy binds `*:8317` (#475) — want loopback/auth mode.

**PeaProxy:** default bind 127.0.0.1; optional LAN + auth token; visible in UI Settings.

### 5. UI that isn't a macOS tray minefield — shipped by design

Menu bar never appears / TCC entitlements (#528), Gatekeeper (#398), dashboard management API disabled by default (#354), Gemini OAuth flag skew (#457), status not refreshing (#415).

**PeaProxy:** CLI + localhost UI only; **no tray** (locked decision, not a gap). Management API on for the local UI; health/status live. 1.0 will not add a tray.

### 6. Multimodal / images — vision-in shipped; image-out gated

Image generation support lagging bundled binary (#343); GPT image requests (#2940 upstream).

**PeaProxy:** vision-in Showcase; `image_out` tagged from live catalog then **gated** (no `/v1/images/generations`, no fake chat drawing). Optional for 1.0 — [V1.md](V1.md).

### 7. Account / credential reliability — shipped (keychain)

OAuth flags missing after updates (#351, #360); credentials disappearing after refresh (#6119); Qwen token refresh fails (#2718); can't re-enable sole disabled account (#326).

**PeaProxy:** OS keychain / encrypted-file store; YAML lists accounts without tokens; refresh writes the secret store. Qwen OAuth is **not yet** (no CPA flow) — use an API key, so there is no Qwen token refresh to fail.

### 8. Harness / client onboarding — shipped

Factory Droid model config confusion (#467); OpenCode Go subscription request (#405); Cline as service (#490); Amp failures; usage stats (#178).

**PeaProxy:** Clients page with copy-paste presets + verify; usage showcase per provider. OpenCode Go subscription and Droid are **not** adapters. Cline is an OpenAI-compatible **client**, not a cloud provider.

### 9. Custom / free / local providers — shipped

Custom provider FR (#347 VibeProxy); local-model flag exists upstream but not productized.

**PeaProxy:** first-class free/local tier (Ollama, LM Studio, llama.cpp, vLLM, Jan, GPT4All, OpenRouter free, OpenCode Zen, HF, Groq, Cerebras, NIM, Workers AI, Ollama Cloud, SambaNova) + filters + listing-only hide. GitHub Models is retired. See [PROVIDERS.md](PROVIDERS.md).

## Must-have differentiators (v0.2.6)

1. Auto model discovery — **yes**
2. Free + paid providers with catalog filters/hide — **yes**
3. Loopback-secure by default — **yes**
4. Harness quick-setup (OpenCode, Pi, Cursor, Claude Code, Codex, Continue, Cline, Amp) — **yes** (Droid no)
5. Reliable multi-account failover with visible cooldowns — **yes** (`round-robin` / `fill-first` / `sticky`)
6. Per-provider usage showcase (text + vision-in; image-out gated) — **yes / gated**
7. Cross-platform CLI + localhost UI (no tray) — **yes, by design**
8. Protocol profiles that don't break thinking/tools/cloak for non-official clients — **defaults off**; not a full translator golden-test suite

## Nice-to-have (not 1.0 blockers)

- Usage statistics persistence — **shipped** (`usage.json`)
- Embeddings endpoint — not shipped
- Image generations API — gated / not proxied
- Quota remaining API when provider exposes it — not shipped
