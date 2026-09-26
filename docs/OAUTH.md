# OAuth

PeaProxy only implements **official, publicly documented** OAuth. There is no reverse-engineered Claude, Codex, or ChatGPT subscription client in this tree.

## Status

| Adapter | Status | What to use instead |
|---|---|---|
| `anthropic_oauth` | Stub (`ErrNotImplemented`) | Official API key → adapter `anthropic` ([console](https://console.anthropic.com/settings/keys), [docs](https://docs.anthropic.com/en/api/getting-started)) |
| `openai_oauth` | Stub (`ErrNotImplemented`) | Platform API key → adapter `openai` ([keys](https://platform.openai.com/api-keys)) |

`peaproxy auth login --provider anthropic|openai` prints the same guidance. `AuthStart` wraps `ErrNotImplemented` with those URLs.

Claude Pro/Max and ChatGPT/Codex consumer OAuth are **not** public third-party APIs. Harvesting desktop client secrets is out of scope.

## Keys that *are* documented (v0.1)

| Need | Adapter | Env |
|---|---|---|
| Claude Messages | `anthropic` | `ANTHROPIC_API_KEY` |
| OpenAI | `openai` | `OPENAI_API_KEY` |
| Google AI Studio / Gemini | `google` or `gemini` (official OpenAI-compat) | `GEMINI_API_KEY` |
| xAI Grok | `xai` | `XAI_API_KEY` |
| Groq | `groq` | `GROQ_API_KEY` |
| Cerebras | `cerebras` | `CEREBRAS_API_KEY` |
| Hugging Face router | `huggingface` | `HF_TOKEN` |
| OpenRouter | `openrouter` | `OPENROUTER_API_KEY` |
| OpenCode Zen | `opencode_zen` | `OPENCODE_API_KEY` from [opencode.ai](https://opencode.ai/docs/zen/) |

If a P0 later publishes a real device-code or public OAuth client for third-party local proxies, that adapter can land behind the existing `Authenticator` interface. Until then, stubs stay stubs.
