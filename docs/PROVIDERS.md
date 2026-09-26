# Providers

Live `ListModels` is the source of truth. This page is a **tier map**, not an allowlist of model IDs. When a provider adds a model, PeaProxy should show it without a code change.

Adapters in this repo today:

| Adapter | Status | Typical auth | Default |
|---|---|---|---|
| `ollama` | List/Chat HTTP client (needs a running Ollama) | none | `http://127.0.0.1:11434/v1` |
| `openai_compat` | Generic base URL + optional key | none / API key | required `baseURL` |
| `anthropic_oauth` | **Stub** — `ErrNotImplemented` | OAuth (TODO) | — |
| `openai_oauth` | **Stub** — `ErrNotImplemented` | OAuth (TODO) | — |

## Paid / subscription

### P0 OAuth (maximize — not in this scaffold)

| Provider | Notes |
|---|---|
| Anthropic Claude | Subscription OAuth stub: `anthropic_oauth` |
| OpenAI ChatGPT / Codex | Subscription OAuth stub: `openai_oauth` |
| Google Gemini | (+ Antigravity if distinct) — no adapter yet |
| xAI Grok | no adapter yet |
| Moonshot Kimi | no adapter yet |
| Qwen consumer OAuth | no adapter yet |

### P0 API keys

Anthropic, OpenAI, Google AI Studio, xAI, Z.AI, OpenRouter, any OpenAI-compat `baseURL` via `openai_compat`.

### P1+

Cline, OpenCode Go subscription, GitHub Copilot, Factory/Amp — as adapters prove out.

## Free / open (first-class)

| Provider | How | Auth | Catalog tier |
|---|---|---|---|
| Ollama | OpenAI-compat `localhost:11434` | none | `local` |
| LM Studio | OpenAI-compat local server (`:1234/v1`) | none / optional | `local` |
| llama.cpp / vLLM server | OpenAI-compat | none | `local` |
| OpenRouter free models | API key; tag `:free` / `pricing=free` | key | `free` |
| GitHub Models | GitHub token | token | `free` |
| Hugging Face Inference / router | token | token | `free` |
| Google AI Studio | API key | key | `free` / `freemium` (quota) |
| Groq and other freemium | key | key | `freemium` |
| Cerebras | key | key | `freemium` |
| NVIDIA NIM free | key | key | `free` |
| Ollama Cloud free plan | account / key | varies | `free` |
| Cloudflare Workers AI | token where applicable | token | `free` |
| OpenCode free routes | per their docs | varies | named preset later |

## Catalog UX

Filters: **All | Free | Paid | Local | Subscription OAuth**.

- Hide provider (dropped from `/v1/models` and UI pickers).
- Hide model (per-id).
- Expose-to-clients subset (coding tools can see less than PeaProxy knows).

`FilterFree` includes `free` **and** `freemium`. Paid is subscription/key hosted. Local is process-on-machine.

## ToS

Using a local proxy with **consumer subscription OAuth** may violate a provider’s terms. PeaProxy will keep that warning in the README. Official OAuth (user-consented, no harvested secrets) only — no reverse-engineered private clients in-tree until a spike explicitly decides otherwise.
