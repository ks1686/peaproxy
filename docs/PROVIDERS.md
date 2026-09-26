# Providers

Live `ListModels` is the source of truth. This page is a **tier map**, not an allowlist of model IDs.

Plan phases and residuals: [PLAN.md](PLAN.md). 1.0 cut: [V1.md](V1.md). Subscription OAuth liability: [OAUTH.md](OAUTH.md). Issue themes vs shipped: [COMPETITOR-WINS.md](COMPETITOR-WINS.md).

Adapters in this repo today (v0.2.6):

| Adapter | Status | Typical auth | Default |
|---|---|---|---|
| `ollama` | List + chat + stream (`/v1` with `/api/tags` fallback) | none | `http://127.0.0.1:11434/v1` |
| `lmstudio` | Thin OpenAI-compat wrapper | none | `http://127.0.0.1:1234/v1` |
| `llamacpp` | Thin OpenAI-compat wrapper | none | `http://127.0.0.1:8080/v1` |
| `vllm` | Thin OpenAI-compat wrapper | none | `http://127.0.0.1:8000/v1` |
| `jan` | Thin OpenAI-compat wrapper | none / optional | `http://127.0.0.1:1337/v1` |
| `gpt4all` | Thin OpenAI-compat wrapper | none | `http://127.0.0.1:4891/v1` |
| `ollama_cloud` | Thin OpenAI-compat wrapper (hosted, not local) | API key | `https://ollama.com/v1` |
| `groq` | Thin OpenAI-compat wrapper | API key | `https://api.groq.com/openai/v1` |
| `cerebras` | Thin OpenAI-compat wrapper | API key | `https://api.cerebras.ai/v1` |
| `google` / `gemini` | Thin OpenAI-compat wrapper | API key | `https://generativelanguage.googleapis.com/v1beta/openai` |
| `xai` | Thin OpenAI-compat wrapper | API key | `https://api.x.ai/v1` |
| `huggingface` | Thin OpenAI-compat wrapper | token | `https://router.huggingface.co/v1` |
| `nim` | Thin OpenAI-compat wrapper | API key | `https://integrate.api.nvidia.com/v1` |
| `workers_ai` | Thin OpenAI-compat wrapper | API token + account id | `https://api.cloudflare.com/client/v4/accounts/YOUR_ACCOUNT_ID/ai/v1` |
| `sambanova` | Thin OpenAI-compat wrapper | API key | `https://api.sambanova.ai/v1` |
| `openai` | First-class OpenAI API (models + chat completions stream) | API key | `https://api.openai.com/v1` |
| `anthropic` | Native Messages (`x-api-key`) + OpenAI chat/completions bridge; true SSE | API key | `https://api.anthropic.com` |
| `openrouter` | Preset; ids ending `:free` tagged free; live list | API key | `https://openrouter.ai/api/v1` |
| `openai_compat` | Generic base URL + optional key, stream + non-stream | none / API key | required `baseURL` |
| `opencode_zen` | Named Zen client (CPA declined #6018) | official API key (preferred) | `https://opencode.ai/zen/v1` |
| `anthropic_oauth` | Claude Pro/Max subscription OAuth + Messages (Bearer). **ToS/ban risk.** | OAuth (`peaproxy auth login --provider anthropic`) | `https://api.anthropic.com` |
| `openai_oauth` | ChatGPT/Codex subscription OAuth + Responses→chat. **ToS/ban risk.** | OAuth (`peaproxy auth login --provider openai`) | `https://chatgpt.com/backend-api/codex` |
| `antigravity` / `gemini_oauth` | Gemini consumer / Antigravity Cloud Code OAuth + generateContent. **ToS/ban risk.** Distinct from AI Studio keys. | OAuth (`--provider gemini`) | `https://cloudcode-pa.googleapis.com` |
| `xai_oauth` | xAI Grok subscription device OAuth + CLI chat proxy. **ToS/ban risk.** | OAuth (`--provider xai`) | `https://cli-chat-proxy.grok.com/v1` |
| `kimi_oauth` / `kimi_ai_oauth` | Moonshot Kimi device OAuth + coding API. **ToS/ban risk.** | OAuth (`--provider kimi` / `kimi-ai`) | `https://api.kimi.com/coding/v1` |
| `meta_oauth` | Meta Muse device OAuth + minted key. **ToS/ban risk.** | OAuth (`--provider meta`) | `https://api.meta.ai/v1` |
| `qwen_oauth` | Stub: CPA has no Qwen consumer OAuth | n/a | use `openai_compat` + a Qwen key |

Hosted/local wrappers live in `internal/adapter/hosted`. They fill the default base URL and catalog tier, then delegate to `openai_compat`.

## Gemini / Google AI Studio

**Official OpenAI-compat is used.** Google documents an OpenAI-compatible Gemini endpoint ([OpenAI compatibility](https://ai.google.dev/gemini-api/docs/openai)):

`https://generativelanguage.googleapis.com/v1beta/openai`

PeaProxy **key** adapters do **not** call `generateContent`. Set `GEMINI_API_KEY` and adapter `google` or alias `gemini`.

**Gemini consumer / Antigravity subscription OAuth** is a separate adapter (`antigravity`, CLI `--provider gemini`). It uses Google OAuth for the public Antigravity IDE client and Cloud Code `generateContent`. **ToS/ban risk** — [OAUTH.md](OAUTH.md). Prefer the AI Studio key.

## Paid / subscription

**P0 OAuth (shipped):** Anthropic Claude Pro/Max, OpenAI ChatGPT/Codex, Google Gemini / Antigravity, xAI Grok, Moonshot Kimi, Meta Muse. **May violate ToS; authors are not liable** — [OAUTH.md](OAUTH.md). Prefer official API keys.

**P0 OAuth (not yet):** Qwen consumer OAuth is stubbed (no CPA flow). Do not reverse-engineer a new flow for 1.0. Devin skipped (not a generic chat upstream).

**P0 keys:** `anthropic`, `openai`, `google`/`gemini`, `xai`, `groq`, `cerebras`, `huggingface`, `nim`, `sambanova`, `openrouter`, `workers_ai`, `ollama_cloud`. Z.AI / others: `openai_compat` + their OpenAI-compat base URL (no first-class preset).

Do **not** advertise Claude Free OAuth (CPA #6016).

## Free / open (first-class)

| Provider | How | Auth | Catalog tier |
|---|---|---|---|
| Ollama | `localhost:11434/v1` | none | `local` |
| LM Studio | `:1234/v1` (`adapter: lmstudio`) | none / optional | `local` |
| llama.cpp | `:8080/v1` (`adapter: llamacpp`) | none / optional | `local` |
| vLLM | `:8000/v1` (`adapter: vllm`) | none / optional | `local` |
| Jan | `:1337/v1` (`adapter: jan`) | none / optional | `local` |
| GPT4All | `:4891/v1` (`adapter: gpt4all`) | none | `local` |
| OpenRouter `:free` | adapter `openrouter` | key | `free` (ids ending `:free`) |
| **OpenCode Zen free** | `https://opencode.ai/zen/v1` | official key from [opencode.ai](https://opencode.ai/docs/zen/); community empty Bearer + `x-session-id` is ToS-fragile | `free` for `-free` / named free ids |
| Hugging Face Inference | `adapter: huggingface` router | token | `freemium` |
| Google AI Studio | official OpenAI-compat Gemini | free key | `freemium` |
| Groq / Cerebras | first-class wrappers | key | `freemium` |
| NVIDIA NIM (API catalog) | `adapter: nim` → `https://integrate.api.nvidia.com/v1` | `NVIDIA_API_KEY` from [build.nvidia.com](https://build.nvidia.com/settings) | `freemium` |
| Cloudflare Workers AI | `adapter: workers_ai` | `CLOUDFLARE_API_TOKEN`; paste the account id into Accounts (or set `CLOUDFLARE_ACCOUNT_ID`) so `YOUR_ACCOUNT_ID` is replaced. `wrangler whoami` / dashboard. | `freemium` |
| Ollama Cloud | `adapter: ollama_cloud` → `https://ollama.com/v1` | `OLLAMA_API_KEY` | `freemium` |
| SambaNova Cloud | `adapter: sambanova` → `https://api.sambanova.ai/v1` | `SAMBANOVA_API_KEY` | `freemium` |

**GitHub Models is retired (2026-07-30).** Do not ship it. Migrate narrative: Azure AI Foundry (paid) or Copilot OAuth (separate, later).

**Not shipped (audit, 2026-09-26):** LocalAI defaults to `:8080/v1`, same as llama.cpp — use the llama.cpp or custom OpenAI-compat preset. Together AI (`https://api.together.ai/v1`) is OpenAI-compat but pay-per-token, not a free inference tier. SGLang / TGI: no extra preset; use custom OpenAI-compat with the port you launched. Accounts shows the env var **name** a preset expects (`envKey` / `accountIDEnv`) and whether it is set in this process; values are never returned.

### OpenCode Zen privacy

Several **free** Zen models may use prompts for training (NVIDIA Nemotron free, Big Pickle, MiMo free, Muse contributor free). PeaProxy shows a privacy note on those catalog rows **and** on the Accounts preset. Prefer an official API key; review [OpenCode Zen docs](https://opencode.ai/docs/zen/) before sending private code.

## Failover

Multiple accounts that list the same model id are tried **round-robin**. HTTP **429** and **401** cool that account down for 30s and try the next one. Health UI shows adapter health (last ListModels/Validate) and remaining cooldown time.

## Catalog UX

Filters: **All | Free | Paid | Local | Subscription OAuth** (persisted in the UI via `localStorage`).

- Hide provider / hide model: omitted from `GET /v1/models` **only**. POST routing still works unless `hide.blockRouting: true` (CPA #5995 / #5349).
- Rich metadata: `GET /v0/catalog` and `GET /admin/catalog` (tier, modalities, privacy).
- Vanilla `/v1/models` stays OpenAI-minimal (`id`, `object`, `owned_by`).
- Models with `image_in` can be tried with an image URL or upload in Showcase.
- Models tagged `image_out` (live `output_modalities` or id enrichment) are **catalogued only**. Showcase shows a gated “not yet”; PeaProxy does not proxy `/v1/images/generations` and will not send a chat completion pretending to draw.

`FilterFree` includes `free` **and** `freemium`.

Claude subscription OAuth may still **403** on Cloudflare with stock Go TLS (no uTLS) — [OAUTH.md](OAUTH.md) residual gaps. No tray UI — [PLAN.md](PLAN.md) locked decisions.
