# PeaProxy — approved plan (2026-09-26)

Status: **v0.2.8 on main**. Phases 0–4 are done. Releases are tagged through **v0.2.8**. This is a usable 0.2.x product, not a scaffold. A **v1.0.0** tag is a honesty/quality cut ([V1.md](V1.md)); that tag is not cut yet.

Residuals (documented, not unmarked phase work):

- **Qwen consumer OAuth:** stubbed **not yet** (no working CPA flow). Use `openai_compat` + a Qwen API key. Do not reverse-engineer a new flow unless a public one exists.
- **Factory/Droid upstream:** stubbed **not yet** (no public consumer chat OAuth). Use Droid as a PeaProxy **client**.
- **Image-out:** **Shipped (1.x).** `POST /v1/images/generations` is proxied for API-key OpenAI-compat adapters when the live catalog tags `image_out`. Showcase one-click generates; subscription OAuth does not invent an image path.
- **Embeddings:** **Shipped (1.x).** `POST /v1/embeddings` is proxied for API-key OpenAI-compat adapters when the live catalog tags `embeddings`. Subscription OAuth does not invent vectors. Quota-remaining remains later.
- **Claude Cloudflare 403:** token exchange uses stock Go `crypto/tls` (no uTLS). Prefer the official Anthropic API-key adapter. Details: [OAUTH.md](OAUTH.md).
- **No tray:** CLI + localhost UI only. That is a locked decision, not a missing feature.

1.0 cut vs already-shippable 0.2.x: [V1.md](V1.md). Liability: [OAUTH.md](OAUTH.md). Adapters: [PROVIDERS.md](PROVIDERS.md).

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
3. **Harness fidelity** — shipped presets + `clients verify [--chat]` for Cursor, Claude Code, OpenCode, Pi, Codex, Continue, Cline, Amp. Cloak defaults off. Amp WebSocket is out of scope.
4. **Secure localhost default** — shipped (`127.0.0.1:8317`; LAN needs `--allow-lan` + admin token).
5. **UI without macOS tray** — shipped **by design** (CLI + browser only).
6. **Credential reliability** — OS keychain / encrypted-file store; YAML lists accounts without printing secrets.
7. **Onboarding** — shipped (Accounts CTAs, Clients copy/verify, Settings, `config validate`). Factory Droid is a **client** preset (`peaproxy clients show droid`); Factory is not a chat-model upstream.
8. **Free + custom providers** — shipped (local + hosted OpenAI-compat presets). GitHub Models retired; not listed.
9. **Multimodal** — vision-in shipped; image-out **proxied** (`POST /v1/images/generations` for keyed OpenAI-compat adapters); embeddings **proxied** (`POST /v1/embeddings`).

## 4. Providers

See [PROVIDERS.md](PROVIDERS.md).

### Paid / subscription (OAuth maximize + keys)

**P0 OAuth (shipped):** Anthropic Claude Pro/Max, OpenAI ChatGPT/Codex, Google Gemini / Antigravity, xAI Grok, Moonshot Kimi, Meta Muse, GitHub Copilot. **ToS/ban risk** — [OAUTH.md](OAUTH.md). Prefer official API keys.

**P0 OAuth (not yet):** Qwen consumer OAuth — stub only. No CPA flow. Factory/Droid consumer chat OAuth — stub only (Droid is a client preset).

**P0 keys (shipped):** `anthropic`, `openai`, `google`/`gemini`, `xai`, `groq`, `cerebras`, `huggingface`, `nim`, `sambanova`, `openrouter`, `workers_ai`, `ollama_cloud`, plus any OpenAI-compat `baseURL`. **Z.AI** has no first-class preset; use `openai_compat`.

**Later (not 1.0 blockers):** Cline-as-cloud. Devin and Vertex are skipped (not generic consumer chat OAuth). **GitHub Copilot OAuth** and **OpenCode Go** (API key, distinct from Zen) are shipped as 1.x leftovers. **Factory/Droid** has no public consumer chat OAuth — stub + Droid harness preset.

### Free / open (first-class)

Each free adapter still uses **live ListModels** — new local pulls appear without PeaProxy releases. Local + hosted presets listed in [PROVIDERS.md](PROVIDERS.md) are registered (v0.2.5).

## 5. Catalog controls (required)

- **Live discovery** source of truth per adapter. ✅
- **Filters:** All | Free | Paid | Local | Subscription OAuth. ✅
- **Hide provider / hide model** — listing-only unless `hide.blockRouting: true`. ✅
- **Expose toggle** — client listing subset. ✅
- Optional **pin/rename** overlays (never routing aliases). ✅
- Tags: `tier` (free\|freemium\|paid\|local), `modalities`, `provider`, `account_id`, `status`. ✅

## 6. Product surface

- Binary: `peaproxy`
- CLI: `serve | auth | accounts | models | catalog | requests | health | status | config | clients` (v0.2.8: `catalog pin|rename|hide`, `requests tail`, `health`, `accounts add`)
- Localhost UI: Accounts (onboarding CTAs), Catalog (filters/hide/pin/rename), Showcase, Clients (harness presets + verify copy), Health (adapter probe + cooldowns), Request log (opt-in), Settings (bind/LAN/secret backend)
- HTTP: OpenAI `/v1/chat/completions`, `/v1/models`, `/v1/images/generations`, `/v1/embeddings`; Claude `/v1/messages`; Codex `/v1/responses`; admin loopback routes

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
5. Releases — **v0.2.0 through v0.2.8 tagged** (GoReleaser linux/darwin/windows amd64+arm64). **v1.0.0** is the next cut ([V1.md](V1.md)); site/marketing is not this repo.

**Shipped in this tree:** native API-key adapters, hosted OpenAI-compat presets, Claude SSE, `/v1/responses`, vision Showcase, **image-out proxy** (`POST /v1/images/generations`), **embeddings proxy** (`POST /v1/embeddings`), failover policies (`round-robin` / `fill-first` / `sticky`) + error-body classification + cooldown-storm skip, persisted usage, request inspector, catalog overlays, Settings/onboarding, `config validate`, CLI catalog/health/requests/`accounts add` (v0.2.8), subscription OAuth (ToS documented), OS keychain / encrypted-file secrets.

## 11. Security

- Default `127.0.0.1:8317`
- Optional bind-all requires explicit flag + UI warning + admin token
- Never log secrets; redact request inspector by default
- OAuth tokens and inline API keys: OS keychain, else AES-GCM file next to config
- README + CLI + UI ToS warning for subscription OAuth; authors are not liable

## 12. Success

**v0.1 / v0.2 — met on main (v0.2.8):**

- ≥2 OAuth + ≥2 API-key + Ollama free
- Live models appear without code change when a provider adds one
- Free/paid/local/subscription_oauth filter + hide provider/model on `/v1/models`
- Cursor + other harness presets with `clients verify --chat`
- Showcase for paid (key or OAuth) and free/local
- `peaproxy serve` one command; first-run config; Settings + onboarding CTAs

**v1.0** is a cut decision, not a new phase: [V1.md](V1.md). Do not treat Qwen OAuth, uTLS, or a tray app as 1.0 blockers. Image-out is a 1.x leftover now shipped. Embeddings is a 1.x leftover now shipped. README screenshot refresh is leftover #3. Quota-remaining remains later.

## 13. Free-provider expansion

Local (no key): Ollama (`:11434/v1`), LM Studio (`:1234/v1`), llama.cpp (`:8080/v1`), vLLM (`:8000/v1`), Jan (`:1337/v1`), GPT4All (`:4891/v1`).

Hosted free / freemium (API key, live `/models`): OpenRouter `:free`, OpenCode Zen free, Hugging Face router, Groq, Cerebras, Google AI Studio free quota, NVIDIA NIM, Ollama Cloud, Cloudflare Workers AI (account id required), SambaNova Cloud. **GitHub Models retired 2026-07-30 — do not list.** Together AI is pay-per-token — skip as a free preset. LocalAI collides with llama.cpp on `:8080` — use llama.cpp or custom `openai_compat`.

Generic `openai_compat` covers the rest with `base_url` + key + tier tag.
