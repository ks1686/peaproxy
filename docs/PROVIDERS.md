# Providers

Live `ListModels` is the source of truth. This page is a **tier map**, not an allowlist of model IDs.

Adapters in this repo today:

| Adapter | Status | Typical auth | Default |
|---|---|---|---|
| `ollama` | List + chat + stream (`/v1` with `/api/tags` fallback) | none | `http://127.0.0.1:11434/v1` |
| `openai_compat` | Generic base URL + optional key, stream + non-stream | none / API key | required `baseURL` |
| `opencode_zen` | Named Zen client (CPA declined #6018) | official API key (preferred) | `https://opencode.ai/zen/v1` |
| `anthropic_oauth` | **Stub** — `ErrNotImplemented` | OAuth (TODO) | — |
| `openai_oauth` | **Stub** — `ErrNotImplemented` | OAuth (TODO) | — |

## Paid / subscription

**P0 OAuth (stubs only):** Anthropic Claude, OpenAI ChatGPT/Codex, later Gemini/Grok/Kimi/Qwen.

**P0 keys:** Anthropic/OpenAI/Google AI Studio/xAI/Z.AI/OpenRouter via `openai_compat` (or native Anthropic later).

Do **not** advertise Claude Free OAuth (CPA #6016).

## Free / open (first-class)

| Provider | How | Auth | Catalog tier |
|---|---|---|---|
| Ollama | `localhost:11434/v1` | none | `local` |
| LM Studio | `:1234/v1` | none / optional | `local` |
| llama.cpp / vLLM | user `baseURL` | none | `local` |
| OpenRouter `:free` | `https://openrouter.ai/api/v1` | key | `free` (ids ending `:free`) |
| **OpenCode Zen free** | `https://opencode.ai/zen/v1` | official key from [opencode.ai](https://opencode.ai/docs/zen/); community empty Bearer + `x-session-id` is ToS-fragile | `free` for `-free` / named free ids |
| Hugging Face Inference | HF router OpenAI-compat | token | `freemium` |
| Google AI Studio | Gemini API / OpenAI-compat if offered | free key | `freemium` |
| Groq / Cerebras / NIM / Workers AI | OpenAI-compat | key | `freemium` |
| Ollama Cloud | hosted OpenAI-compat | account / key | `freemium` |

**GitHub Models is retired (2026-07-30).** Do not ship it. Migrate narrative: Azure AI Foundry (paid) or Copilot OAuth (separate, later).

### OpenCode Zen privacy

Several **free** Zen models may use prompts for training (NVIDIA Nemotron free, Big Pickle, MiMo free, Muse contributor free). PeaProxy shows a privacy note on those catalog rows. Prefer an official API key; document ToS yourself.

## Catalog UX

Filters: **All | Free | Paid | Local | Subscription OAuth**.

- Hide provider / hide model: omitted from `GET /v1/models` **only**. POST routing still works unless `hide.blockRouting: true` (CPA #5995 / #5349).
- Rich metadata: `GET /v0/catalog` and `GET /admin/catalog` (tier, modalities, privacy).
- Vanilla `/v1/models` stays OpenAI-minimal (`id`, `object`, `owned_by`).

`FilterFree` includes `free` **and** `freemium`.
