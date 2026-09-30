# Contributing to PeaProxy

Public repo: https://github.com/ks1686/peaproxy

## Scope right now

**1.6.x** on main (latest GitHub Release **v1.6.9**). Native Anthropic / OpenAI / OpenRouter / OpenCode Zen / OpenCode Go adapters, hosted OpenAI-compat presets (LM Studio, llama.cpp, vLLM, Jan, GPT4All, Groq, Cerebras, Google AI Studio, xAI, Hugging Face, NVIDIA NIM, Cloudflare Workers AI, Ollama Cloud, SambaNova), live catalog (hide/pin/rename), Claude SSE, Responses (`POST /v1/responses`), vision Showcase, image-out / embeddings proxies, quota remaining, opt-in request inspector, multi-account failover, persisted usage, Settings/onboarding, and **subscription OAuth** for Claude, Codex, Gemini/Antigravity, xAI, Kimi, Meta Muse, and GitHub Copilot ([docs/OAUTH.md](docs/OAUTH.md)). Subscription OAuth may violate provider ToS; authors are not liable; prefer official API keys.

Qwen OAuth and Factory/Droid chat-upstream OAuth are stubbed **not yet**. Droid is a harness **client**. Image-out and embeddings are proxied for API-key OpenAI-compat (`POST /v1/images/generations`, `POST /v1/embeddings`). No tray (by design). Phases: [docs/PLAN.md](docs/PLAN.md). 1.0 cut: [docs/V1.md](docs/V1.md).

## Dev loop

```bash
go test ./...
go build ./...
go run ./cmd/peaproxy --help
go run ./cmd/peaproxy --version
```

Default listen address is `127.0.0.1:8317`. Non-loopback bind requires `allowNonLoopback: true` and a non-empty `adminToken`.

Releases: push a `v*` tag. GitHub Actions runs GoReleaser (linux/darwin/windows, amd64+arm64) and stamps `internal/version.Version` into `--version`. Darwin archives are Developer ID signed and notarized when the `MACOS_SIGN_*` / `MACOS_NOTARY_*` secrets are set (see [docs/RELEASING.md](docs/RELEASING.md)); Homebrew cask consumers get those Gatekeeper-clean binaries. Stable tags also push a Homebrew cask to `ks1686/homebrew-tap` when `HOMEBREW_TAP_GITHUB_TOKEN` is set, a Scoop manifest to `ks1686/scoop-bucket` when `SCOOP_BUCKET_GITHUB_TOKEN` is set, and the AUR package `peaproxy-bin` when `AUR_KEY` is set (each publisher skips itself when its secret is missing, so GitHub Release assets always publish). `go install github.com/ks1686/peaproxy/cmd/peaproxy@vX.Y.Z` reads the module version from `runtime/debug.ReadBuildInfo()` when ldflags are unset. Local `go install` builds are unsigned.

## Conventions

- Conventional Commits (`feat:`, `fix:`, `docs:`, `test:`, `ci:`).
- No AI authorship trailers on commits or PRs.
- Live `ListModels` per adapter — never a hand-maintained model allowlist as source of truth.
- Secrets stay in the OS keychain (encrypted file fallback). Never log tokens.
- Never commit `.p8` / `.p12` / passphrases. Release signing secrets belong in GitHub Actions only.
- Prompt-cache-safe JSON: structs + `jsonx.SetStream` / `jsonx.DropTopLevelKeys`, never `map[string]any` for Anthropic or Codex request bodies.
