# PeaProxy — approved plan (2026-09-26)

Status: **2.0.x on main**. Latest GitHub Release is **v2.0.9** (request engine: [V2.md](V2.md)). Phases 0–4 are done. This is a shipped 2.x product, not a 0.2.8 scaffold. **v1.0.0** is an honesty/marketing declaration from current `main` after this docs cut ([V1.md](V1.md)) — not a new 1.6 patch, and not tagged from a feature branch.

Residuals (documented, not unmarked phase work):

- **Qwen consumer OAuth:** stubbed **not yet** (no working CPA flow). Use `openai_compat` + a Qwen API key. Do not reverse-engineer a new flow unless a public one exists.
- **Factory/Droid upstream:** stubbed **not yet** (no public consumer chat OAuth). Use Droid as a PeaProxy **client**.
- **Image-out:** **Shipped.** `POST /v1/images/generations` is proxied for API-key OpenAI-compat adapters when the live catalog tags `image_out`. Showcase one-click generates; subscription OAuth does not invent an image path.
- **Embeddings:** **Shipped.** `POST /v1/embeddings` is proxied for API-key OpenAI-compat adapters when the live catalog tags `embeddings`. Subscription OAuth does not invent vectors.
- **Quota remaining:** **Shipped.** Latest remaining from documented upstream rate-limit headers and OpenRouter `GET /api/v1/key` (Health refresh / probe only). Unknown remaining is omitted — never invented as 0, unlimited, or scraped HTML.
- **Claude Cloudflare 403:** token exchange uses stock Go `crypto/tls` (no uTLS). Prefer the official Anthropic API-key adapter. Details: [OAUTH.md](OAUTH.md).
- **No tray:** CLI + localhost UI only. That is a locked decision, not a missing feature.

1.0 cut vs shipped 1.6.x surface: [V1.md](V1.md). Liability: [OAUTH.md](OAUTH.md). Adapters: [PROVIDERS.md](PROVIDERS.md).

Marketing pages (pea-pod.me) are a **separate repo** and are not a PeaProxy 1.0 blocker.

## 1. One-liner

Local multi-provider AI gateway in Go: subscription OAuth (**at your own risk**) + API keys + **free/local providers**, first-class text + vision-in + image-out + embeddings, **live auto model catalog** (no hand-maintained allowlist), OpenAI-, Claude-, and Responses-compatible endpoints, CLI service + localhost UI, harness quick-setup, per-provider usage.

## 2. Locked decisions

| Decision | Choice |
|---|---|
| Name | PeaProxy |
| Language | Go |
| Repo | Public `ks1686/peaproxy` from day one |
| UI | CLI + localhost only (**no** menu bar / tray) |
| Auth | Subscription OAuth (**maximize, ToS/ban risk**) + API keys + free/local. Keys remain the official path. |
| Multimodal | Vision-in shipped; image-out via live capabilities + `/v1/images/generations`; embeddings via `/v1/embeddings` (API-key OpenAI-compat) |
| Models | Live provider lists only; app updates never required to expose new models |
| Default bind | `127.0.0.1` (LAN optional + auth) |
| Default port | `8317` |
| License | MIT |

## 3. Competitive wins (from VibeProxy + CLIProxyAPI issues)

Must beat them on (see [COMPETITOR-WINS.md](COMPETITOR-WINS.md) for shipped vs residual):

1. **Auto model discovery** — shipped (live `ListModels`, no allowlist).
2. **Failover that works** — shipped: `round-robin` (default), `fill-first`, and `sticky` via `failover.policy`; 30s cooldown skip on retryable status **and** rate-limit / overloaded / auth-expired error bodies (no re-hit storm).
3. **Harness fidelity** — shipped presets + `clients verify [--chat]` for Cursor, Claude Code, OpenCode, Pi, Codex, Continue, Cline, Amp, Droid. Client-preset cloak defaults off. Amp WebSocket is out of scope. **Separate:** `anthropic_oauth` Messages inject Claude Code’s billing header + CLI identity so subscription OAuth is not 429’d as a non-CLI client.
4. **Secure localhost default** — shipped (`127.0.0.1:8317`; LAN needs `--allow-lan` + admin token).
5. **UI without macOS tray** — shipped **by design** (CLI + browser only).
6. **Credential reliability** — OS keychain / encrypted-file store; YAML lists accounts without printing secrets.
7. **Onboarding** — shipped (Accounts CTAs, Clients copy/verify, Settings, `config validate`). Factory Droid is a **client** preset (`peaproxy clients show droid`); Factory is not a chat-model upstream.
8. **Free + custom providers** — shipped (local + hosted OpenAI-compat presets). GitHub Models retired; not listed.
9. **Multimodal** — vision-in shipped; image-out **proxied** (`POST /v1/images/generations` for keyed OpenAI-compat adapters); embeddings **proxied** (`POST /v1/embeddings`).

## 4. Providers

See [PROVIDERS.md](PROVIDERS.md).

### Paid / subscription (OAuth maximize + keys)

**P0 OAuth (shipped):** Anthropic Claude Pro/Max/Team/Enterprise, OpenAI ChatGPT/Codex, Google Gemini / Antigravity, xAI Grok, Moonshot Kimi, Meta Muse, GitHub Copilot. **ToS/ban risk** — [OAUTH.md](OAUTH.md). Prefer official API keys.

**P0 OAuth (not yet):** Qwen consumer OAuth — stub only. No CPA flow. Factory/Droid consumer chat OAuth — stub only (Droid is a client preset).

**P0 keys (shipped):** `anthropic`, `openai`, `google`/`gemini`, `xai`, `groq`, `cerebras`, `huggingface`, `nim`, `sambanova`, `openrouter`, `workers_ai`, `ollama_cloud`, `opencode_zen`, `opencode_go`, plus any OpenAI-compat `baseURL`. **Z.AI** has no first-class preset; use `openai_compat`.

**Later (not 1.0 blockers):** Cline-as-cloud. Devin and Vertex are skipped (not generic consumer chat OAuth). **GitHub Copilot OAuth** and **OpenCode Go** (API key, distinct from Zen) shipped in 1.x. **Factory/Droid** has no public consumer chat OAuth — stub + Droid harness preset.

### Free / open (first-class)

Each free adapter still uses **live ListModels** — new local pulls appear without PeaProxy releases. Local + hosted presets listed in [PROVIDERS.md](PROVIDERS.md) are registered.

## 5. Catalog controls (required)

- **Live discovery** source of truth per adapter. ✅
- **Filters:** All | Free | Paid | Local | Subscription OAuth. ✅
- **Hide provider / hide model** — listing-only unless `hide.blockRouting: true`. ✅
- **Expose toggle** — client listing subset. ✅
- Optional **pin/rename** overlays (never routing aliases). ✅
- Tags: `tier` (free\|freemium\|paid\|local), `modalities`, `provider`, `account_id`, `status`. ✅

## 6. Product surface

- Binary: `peaproxy`
- CLI: `serve | auth | accounts | models | catalog | requests | health | status | config | clients` (`catalog pin|rename|hide`, `requests tail`, `health`, `accounts add`)
- Localhost UI: Accounts (onboarding CTAs), Catalog (filters/hide/pin/rename), Showcase, Clients (harness presets + verify copy), Health (adapter probe + quota remaining + cooldowns), Request log (opt-in), Settings (bind/LAN/secret backend)
- HTTP: OpenAI `/v1/chat/completions`, `/v1/models`, `/v1/images/generations`, `/v1/embeddings`; Claude `/v1/messages`; Codex `/v1/responses`; admin loopback routes
- OAuth wire details (2.0.x): `anthropic_oauth` Messages send Claude Code fingerprint + **system cloak** (billing header + CLI identity; caller system relocated, never deleted). `openai_oauth` Codex Responses force `store: false`, omit `max_output_tokens` / `stream_options`. Translated chat SSE emits `finish_reason` before `[DONE]`. Cross-wire thinking/reasoning is chat `reasoning_opaque` (Anthropic and Responses kinds stay distinct; OpenAI-compat upstreams strip the field).

## 7. Showcase

Per connected provider: one-click text example; if `image_in`, multimodal example (URL or upload); usage grouped by provider and account. Models tagged `image_out` one-click generate via `/v1/images/generations` when the account’s adapter can proxy it (API-key OpenAI / Google / xAI / OpenAI-compat, plus local wrappers). Models tagged `embeddings` try via `/v1/embeddings` the same way. Subscription OAuth does not fake a drawing or vectors as chat.

## 8. Harness quick-setup

See [HARNESS.md](HARNESS.md). Copy-ready configs for Cursor, Claude Code, OpenCode, Pi, Codex, Continue, Cline, Amp, Droid. Each preset: base URL, auth header, cloak default, sample snippet, `peaproxy clients verify <name> [--chat]`.

## 9. Architecture

Clients → Local HTTP (OpenAI + Claude + Responses) → Gateway (model → account, failover.policy, cooldowns) → Adapters (oauth | apikey | openai_compat | local) → upstream.

Adapter contract: `ListModels`, `Chat` (stream/non-stream), `Auth*` / `Validate`, `Capabilities`. Optional `NativeMessages` / `NativeResponses` / `ImageGenerator` / `Embedder`.

Config: versioned YAML + env; secrets in OS keychain with encrypted file fallback.

## 10. Phases

0. Plan lock ✅
1. Spike: 1 OAuth + 1 API-key + Ollama; chat + vision-in; live `/v1/models`; UI + free/paid filter ✅
2. Core P0 key adapters; multi-account failover; CLI; harness presets (Cursor + Claude Code + OpenCode + Pi, later Codex/Continue/Cline/Amp) ✅
3. Catalog polish (hide/filter/pin/rename); showcase; request inspector; health; Settings / first-run onboarding ✅ (v0.2.4–v0.2.6)
4. OAuth maximize + remaining free/local hosted presets ✅ (v0.2.0 OAuth; v0.2.2–v0.2.5 presets). **Qwen consumer OAuth still stubbed.**
5. Releases — **v0.2.0 through v0.2.8, then 1.1.0 through v1.6.9, then v2.0.0 through v2.0.9 tagged** (GoReleaser linux/darwin/windows amd64+arm64; macOS Developer ID + notarization; Homebrew cask `ks1686/tap`). Historic GitHub `v1.0.0` (0.2.8-era docs) predates 1.6.x. **Formal v1.0.0** is the honesty declaration from current `main` after this docs PR ([V1.md](V1.md)); this PR does not cut 1.6.10. Site/marketing is not this repo.

**Shipped in this tree (2.0.x):** native API-key adapters, hosted OpenAI-compat presets, Claude SSE, `/v1/responses`, vision Showcase, **image-out proxy** (`POST /v1/images/generations`), **embeddings proxy** (`POST /v1/embeddings`), **quota remaining** (documented headers + OpenRouter `GET /key`), failover policies (`round-robin` / `fill-first` / `sticky`) + error-body classification + cooldown-storm skip, persisted usage, request inspector, catalog overlays, Settings/onboarding, `config validate`, CLI catalog/health/requests/`accounts add`, subscription OAuth including Copilot (ToS documented), OpenCode Go API key, OS keychain / encrypted-file secrets, Homebrew cask + signed/notarized macOS binaries, `anthropic_oauth` Claude Code fingerprint + system cloak on Messages, `openai_oauth` `store: false` + omit `max_output_tokens`, chat SSE `finish_reason` before `[DONE]`.

## 11. Security

- Default `127.0.0.1:8317`
- Optional bind-all requires explicit flag + UI warning + admin token
- Never log secrets; redact request inspector by default
- OAuth tokens and inline API keys: OS keychain, else AES-GCM file next to config
- README + CLI + UI ToS warning for subscription OAuth; authors are not liable

## 12. Success

**v0.1 / v0.2 — met; product continued as 1.x through v1.6.9:**

- ≥2 OAuth + ≥2 API-key + Ollama free
- Live models appear without code change when a provider adds one
- Free/paid/local/subscription_oauth filter + hide provider/model on `/v1/models`
- Cursor + other harness presets with `clients verify --chat`
- Showcase for paid (key or OAuth) and free/local
- `peaproxy serve` one command; first-run config; Settings + onboarding CTAs

**v1.0** is a cut decision, not a new phase: [V1.md](V1.md). Tag **v1.0.0 from current `main` tip** as the 1.0 declaration; subsequent work stays **1.x.y**. Do not treat Qwen OAuth, uTLS, or a tray app as 1.0 blockers. Do not invent a second 1.6 release from the honesty docs PR.

## 13. Free-provider expansion

Local (no key): Ollama (`:11434/v1`), LM Studio (`:1234/v1`), llama.cpp (`:8080/v1`), vLLM (`:8000/v1`), Jan (`:1337/v1`), GPT4All (`:4891/v1`).

Hosted free / freemium (API key, live `/models`): OpenRouter `:free`, OpenCode Zen free, Hugging Face router, Groq, Cerebras, Google AI Studio free quota, NVIDIA NIM, Ollama Cloud, Cloudflare Workers AI (account id required), SambaNova Cloud. **GitHub Models retired 2026-07-30 — do not list.** Together AI is pay-per-token — skip as a free preset. LocalAI collides with llama.cpp on `:8080` — use llama.cpp or custom `openai_compat`.

Generic `openai_compat` covers the rest with `base_url` + key + tier tag.
