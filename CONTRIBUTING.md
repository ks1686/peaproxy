# Contributing to PeaProxy

Public repo: https://github.com/ks1686/peaproxy

## Scope right now

Native Anthropic / OpenAI / OpenRouter / OpenCode Zen adapters, live catalog, Claude SSE, vision Showcase, multi-account failover, persisted usage. OAuth adapters (`anthropic_oauth`, `openai_oauth`) remain stubs. Do not land reverse-engineered login flows or harvested client secrets.

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
- Prompt-cache-safe JSON: structs + `jsonx.SetStream`, never `map[string]any` for Anthropic bodies.
