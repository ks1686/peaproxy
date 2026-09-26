# Competitor issue wins → PeaProxy design goals

Sources: open/recent issues on automazeio/vibeproxy and router-for-me/CLIProxyAPI (sampled 2026-09-26). These are **product goals**, not a claim that the scaffold already ships them.

## Themes PeaProxy must beat

### 1. Live model catalog (no hand maintenance)

CLIProxyAPI repeatedly gets "add model X" issues (Opus 4.6/4.7, Fable, GPT Daybreak, Gemini Flash variants, Devin SWE models missing from catalog #6111 regression). Hand-maintained registries go stale.

**PeaProxy:** adapters always list from provider live endpoints; never ship allowlists as source of truth; enrich optionally.

### 2. Failover that actually fails over

#6135 failover broken when quota exhausted; #6130 scheduler can't terminal-reject; cooldown/429 storms (#1015, #903).

**PeaProxy:** explicit failover policies (round-robin, fill-first, sticky); terminal "no account"; clear cooldown UX; error-code → next credential.

### 3. Protocol fidelity for coding harnesses

- Claude cloak defaults break Pi / non-Claude-Code (#6120)
- Thinking/clear_thinking injection breaks plain Claude (#509 VibeProxy)
- Tool-result adjacency / translator bugs (#6129, #4112)
- Cursor tool blocks on OpenAI wire (#411 VibeProxy)
- Codex Responses vs Messages quirks; image_gen tool conflicts (#456)
- Amp WebSocket / stream_options failures

**PeaProxy:** first-class harness profiles (Pi, OpenCode, Cursor, Claude Code, Codex); documented cloak/thinking defaults; golden tests per harness.

### 4. Security defaults

VibeProxy ThinkingProxy binds `*:8317` (#475) — want loopback/auth mode.

**PeaProxy:** default bind 127.0.0.1; optional LAN + auth token; visible in UI.

### 5. UI that isn't a macOS tray minefield

Menu bar never appears / TCC entitlements (#528), Gatekeeper (#398), dashboard management API disabled by default (#354), Gemini OAuth flag skew (#457), status not refreshing (#415).

**PeaProxy:** CLI + localhost UI only; management API on by default for local UI; health/status live.

### 6. Multimodal / images

Image generation support lagging bundled binary (#343); GPT image requests (#2940 upstream).

**PeaProxy:** vision-in v1; image-out as capability flag from live catalog; showcase per provider.

### 7. Account / credential reliability

OAuth flags missing after updates (#351, #360); credentials disappearing after refresh (#6119); Qwen token refresh fails (#2718); can't re-enable sole disabled account (#326).

**PeaProxy:** atomic credential store; merge attrs on refresh; disable/enable without lockout; refresh status in UI.

### 8. Harness / client onboarding

Factory Droid model config confusion (#467); OpenCode Go subscription request (#405); Cline as service (#490); Amp failures; usage stats (#178).

**PeaProxy:** Clients page with copy-paste presets + verify; usage showcase per provider.

### 9. Custom / free / local providers

Custom provider FR (#347 VibeProxy); local-model flag exists upstream but not productized.

**PeaProxy:** first-class free tier (Ollama, LM Studio, llama.cpp, OpenRouter free, OpenCode Zen, HF, …) + free/paid filter + listing-only hide. GitHub Models is retired.

## Must-have differentiators

1. Auto model discovery
2. Free + paid providers with catalog filters/hide
3. Loopback-secure by default
4. Harness quick-setup (OpenCode, Pi, Cursor, Claude Code, Codex, …)
5. Reliable multi-account failover with visible cooldowns
6. Per-provider usage showcase (text + image when capable)
7. Cross-platform CLI + localhost UI (no tray)
8. Protocol profiles that don't break thinking/tools/cloak for non-official clients

## Nice-to-have

- Usage statistics persistence
- Embeddings endpoint
- Image generations API
- Quota remaining API when provider exposes it
