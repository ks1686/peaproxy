# PeaProxy — approved plan (2026-09-26)

Status: **APPROVED**. Native API-key adapters shipped. **Subscription OAuth shipped 2026-09-26** (Claude, Codex, Gemini/Antigravity, xAI, Kimi, Meta Muse) under an explicit owner ToS/ban-risk override ([OAUTH.md](OAUTH.md)). **v0.2.0 tagged from that commit.** Qwen consumer OAuth is stubbed not-yet. Post-v0.2: OS keychain / encrypted-file secrets wired.

Overnight scope that produced this repo: research → incorporate free providers → scaffold. No pea-pod marketing page until a real release exists.

## 1. One-liner

Local multi-provider AI gateway in Go: maximize subscription OAuth + API keys + **free/local providers**, first-class text + images, **live auto model catalog** (no hand-maintained allowlist), OpenAI- and Claude-compatible endpoints, CLI service + localhost UI, harness quick-setup, per-provider usage showcase. Pea Pod OSS → pea-pod.me/peaproxy when shippable.

## 2. Locked decisions

| Decision | Choice |
|---|---|
| Name | PeaProxy |
| Language | Go |
| Repo | Public `ks1686/peaproxy` from day one |
| UI | CLI + localhost only (no menu bar / tray) |
| Auth | Subscription OAuth (**maximize**) + API keys + free/local |
| Multimodal | Vision in v1; image-out later via live capabilities |
| Models | Live provider lists only; app updates never required to expose new models |
| Default bind | `127.0.0.1` (LAN optional + auth) |
| Default port | `8317` |
| License | MIT |

## 3. Competitive wins (from VibeProxy + CLIProxyAPI issues)

Must beat them on:

1. **Auto model discovery** — stop the endless “add model X” treadmill (CLIProxyAPI catalog regressions; dozens of Claude/GPT/Gemini add-model issues).
2. **Failover that works** — quota/429 → next credential without manual disable (cooldown storms).
3. **Harness fidelity** — Pi cloak defaults, thinking injection breaking plain Claude, Cursor tools, Amp/Codex quirks.
4. **Secure localhost default** — not `*:8317`.
5. **UI without macOS tray pain** — TCC/menu bar, Gatekeeper, disabled management API.
6. **Credential reliability** — refresh wiping UI entries, missing login flags after updates.
7. **Onboarding** — OpenCode/Pi/Cursor/Claude Code/Codex/Droid presets.
8. **Free + custom providers** — first-class, not an afterthought.
9. **Multimodal** — vision + clear image-gen path.

Mined issue themes (design goals, not a claim that PeaProxy already ships them): [COMPETITOR-WINS.md](COMPETITOR-WINS.md). The scaffold encodes filters, loopback bind, failover policy types, and harness preset names.

## 4. Providers

See [PROVIDERS.md](PROVIDERS.md).

### Paid / subscription (OAuth maximize + keys)

**P0 OAuth:** Anthropic Claude, OpenAI ChatGPT/Codex, Google Gemini (+ Antigravity if distinct), xAI Grok, Moonshot Kimi, Qwen consumer OAuth.

**P0 keys:** Anthropic, OpenAI, Google AI Studio, xAI, Z.AI, OpenRouter, any OpenAI-compat base URL.

**P1+:** Cline, OpenCode Go subscription, GitHub Copilot, Factory/Amp paths as adapters prove out; long-tail from CLIProxy community.

### Free / open (first-class)

Each free adapter still uses **live ListModels** — new local pulls appear without PeaProxy releases.

## 5. Catalog controls (required)

- **Live discovery** source of truth per adapter.
- **Filters:** All | Free | Paid | Local | Subscription OAuth.
- **Hide provider** — excluded from `/v1/models` and UI pickers (still configurable in Settings).
- **Hide model** — per-id hide list.
- **Expose toggle** — “what coding tools see” can be a subset of “what PeaProxy knows.”
- Optional rename/pin; never required for discovery.
- Tags on each model: `tier` (free\|freemium\|paid\|local), `modalities`, `provider`, `account_id`, `status`.

Implemented: `internal/catalog` (+ tests) with live merge from adapters in `serve`, listing-only hide, and optional pin/rename overlays.

## 6. Product surface

- Binary: `peaproxy`
- CLI: `serve | auth | accounts | models | status | config | clients`
- Localhost UI: Accounts, Catalog (filters/hide), Showcase (try each provider), Clients (harness presets), Health, Request log (opt-in), Settings
- HTTP: OpenAI `/v1/chat/completions`, `/v1/models`; Claude `/v1/messages`; admin loopback routes

## 7. Showcase

Per connected provider: one-click text example; if `image_in`, multimodal example; show raw request/response (redacted). Docs should mirror the same examples. **UI page is a placeholder.**

## 8. Harness quick-setup

See [HARNESS.md](HARNESS.md). Copy-ready configs for Cursor, Claude Code, OpenCode, Pi, Codex, Continue, Cline, Amp. Each preset: base URL, auth header, cloak default, sample snippet, `peaproxy clients verify <name> [--chat]` smoke.

## 9. Architecture

Clients → Local HTTP (OpenAI + Claude) → Router (model → account, failover) → Adapters (oauth | apikey | openai_compat | local) → upstream.

Adapter contract: `ListModels`, `Chat` (stream/non-stream), `Auth*` / `Validate`, `Capabilities`.

Config: versioned YAML + env; secrets in OS keychain with encrypted file fallback (**wired post-v0.2**).

## 10. Phases

0. Plan lock ✅
1. Spike: 1 OAuth + 1 API-key + Ollama; chat + image; live `/v1/models`; minimal UI + free/paid filter stub ✅
2. Core P0 providers; multi-account failover; CLI; harness presets for Cursor + Claude Code + OpenCode + Pi ✅
3. Catalog polish (hide/filter); showcase; request log; health ✅
4. OAuth maximize + more free adapters ✅ (Qwen consumer OAuth still stubbed)
5. Releases + pea-pod.me/peaproxy page — **v0.2.0 tagged**; marketing page is a separate repo

**This tree implements native API-key adapters (Anthropic, OpenAI, OpenRouter, Zen), Claude SSE, vision Showcase, failover + persisted usage, release scaffolding, subscription OAuth for Claude, Codex, Gemini/Antigravity, xAI, Kimi, and Meta Muse (ToS risk documented), and OS keychain / encrypted-file secret storage.**

## 11. Security

- Default `127.0.0.1:8317`
- Optional bind-all requires explicit flag + UI warning + admin token
- Never log secrets; redact request inspector by default
- OAuth tokens and inline API keys: OS keychain, else AES-GCM file next to config
- README ToS warning for subscription OAuth

## 12. Success (v0.1 — met; v0.2 adds OAuth maximize + secret store)

- ≥2 OAuth + ≥2 API-key + Ollama free
- Live models appear without code change when provider adds one
- Free/paid filter + hide provider/model works on `/v1/models`
- Cursor + one other harness preset verified
- Showcase works for at least one paid and one free provider
- `peaproxy serve` one command

## 13. Free-provider expansion

Local (no key): Ollama (`:11434/v1`), LM Studio (`:1234/v1`), llama.cpp (`:8080/v1`), vLLM (`:8000/v1`), Jan (`:1337/v1`), GPT4All (`:4891/v1`).

Hosted free / freemium (API key, live `/models`): OpenRouter `:free` models, OpenCode Zen free, Hugging Face router, Groq, Cerebras, Google AI Studio free quota, NVIDIA NIM free, Ollama Cloud free plan, Cloudflare Workers AI (account id required), SambaNova Cloud. **GitHub Models retired 2026-07-30 — do not list.** Together AI is documented OpenAI-compat but pay-per-token — skip as a free preset.

Generic `openai_compat` adapter covers most with `base_url` + key + tier tag inferred from provider metadata or user label.
