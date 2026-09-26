# Contributing to PeaProxy

Public repo: https://github.com/ks1686/peaproxy

## Scope right now

Native Anthropic / OpenAI / OpenRouter / OpenCode Zen adapters, hosted OpenAI-compat presets (LM Studio, llama.cpp, vLLM, Jan, GPT4All, Groq, Cerebras, Google AI Studio, xAI, Hugging Face, NVIDIA NIM, Cloudflare Workers AI, Ollama Cloud, SambaNova), live catalog (hide/pin/rename), Claude SSE, Responses (`POST /v1/responses`), vision Showcase, opt-in request inspector, multi-account failover, persisted usage, and **subscription OAuth** for Claude, Codex, Gemini/Antigravity, xAI, Kimi, and Meta Muse ([docs/OAUTH.md](docs/OAUTH.md)). Subscription OAuth may violate provider ToS; authors are not liable; prefer official API keys.

## Dev loop

```bash
go test ./...
go build ./...
go run ./cmd/peaproxy --help
go run ./cmd/peaproxy --version
```

Default listen address is `127.0.0.1:8317`. Non-loopback bind requires `allowNonLoopback: true` and a non-empty `adminToken`.

Releases: push a `v*` tag. GitHub Actions runs GoReleaser (linux/darwin/windows, amd64+arm64) and stamps `internal/version.Version` into `--version`.

## Conventions

- Conventional Commits (`feat:`, `fix:`, `docs:`, `test:`, `ci:`).
- No AI authorship trailers on commits or PRs.
- Live `ListModels` per adapter — never a hand-maintained model allowlist as source of truth.
- Secrets stay in the OS keychain (encrypted file fallback). Never log tokens.
- Prompt-cache-safe JSON: structs + `jsonx.SetStream` / `jsonx.DropTopLevelKeys`, never `map[string]any` for Anthropic or Codex request bodies.
