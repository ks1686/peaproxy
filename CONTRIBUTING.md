# Contributing to PeaProxy

Public repo: https://github.com/ks1686/peaproxy

## Scope right now

This tree is a **v0 scaffold**. OAuth adapters (`anthropic_oauth`, `openai_oauth`) are stubs. Do not land reverse-engineered login flows or harvested client secrets.

## Dev loop

```bash
go test ./...
go build ./...
go run ./cmd/peaproxy --help
```

Default listen address is `127.0.0.1:8317`. Non-loopback bind requires `allowNonLoopback: true` and a non-empty `adminToken`.

## Conventions

- Conventional Commits (`feat:`, `fix:`, `docs:`, `test:`, `ci:`).
- No AI authorship trailers on commits or PRs.
- Live `ListModels` per adapter — never a hand-maintained model allowlist as source of truth.
- Secrets stay in the OS keychain (encrypted file fallback). Never log tokens.

## Spike next (not this PR)

1. One real OAuth adapter + one API-key adapter + Ollama.
2. Chat + vision-in; live `/v1/models`.
3. Failover on 429/quota.
4. Harness verify for Cursor + one other client.
