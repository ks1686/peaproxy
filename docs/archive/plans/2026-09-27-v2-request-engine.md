# PeaProxy v2.0.0 — request-engine implementation plan

Status: **superseded** by [the final plan](2026-09-27-v2-request-engine-final.md), shipped in v2.0.10.
Living document: [../../V2.md](../../V2.md).

## Goal

Make existing harness requests more compatible, reliable and efficient without asking users
to redesign their workflows. The request path remains: harness → PeaProxy → selected native
provider or faithful translation → harness.

## Constraints

- Preserve exact-model intent, raw opaque protocol fields and live model discovery.
- Never silently weaken an unsupported request merely to return HTTP 200.
- Never replay a request after a meaningful stream event commits to the client.
- Bound retries, queues, caches and stream buffers; propagate cancellation upstream.
- Keep credentials out of client configuration and logs.
- Additive configuration preserves schema-1 config behavior.

## Ordered workstreams

1. **Serial: conformance baseline.** Characterize OpenAI Chat, Anthropic Messages and
   Responses behavior with local fake upstreams. Lock down alias echo, tool-call argument
   association, opaque reasoning, stream lifecycle, cancellations and images/embeddings.
   Add same-host pass-through benchmarks before optimization.
2. **Serial: attempt engine.** Introduce request metadata and a shared attempt coordinator
   across chat/messages/responses. Enforce total deadline and attempt budget; distinguish
   retryable, delivered and ambiguous requests.
3. **Serial: fidelity and state.** Add capability/requirement evidence, protocol-specific
   translation behavior and account-bound continuation state.
4. **Serial: resilient streams and routing.** Validate bounded stream prelude before commit;
   add scoped cooldowns, admission leases, adaptive account selection, refresh coordination
   and transport cancellation.
5. **Serial: invisible optimization.** Preserve documented prompt-cache directives, handle
   local cold starts, add explicit automatic routes and narrowly eligible exact caching.
6. **Parallel-safe after request contracts stabilize: harness integrations.** Add transactional
   detect/connect/disconnect support for supported coding tools while preserving unrelated
   settings. Integrate serially with the UI.
7. **Serial: qualification.** Run cross-platform and installed-harness verification, benchmark
   comparisons, review and applicable security scans before a separately authorized release.

## Task 01: conformance baseline

Files:

- `internal/server/conformance_test.go`: real server plus fake OpenAI-compatible upstreams;
  Responses tool delta association, Claude lifecycle events and route alias contract.
- `internal/gateway/benchmark_test.go`: native pass-through baseline for 1 KiB, 100 KiB and
  1 MiB requests.

The characterization tests are expected to pass before refactoring. They prove that later
production changes did not break known behavior. Benchmark results are captured locally and
not committed as a universal performance claim.

Proof:

```sh
PEAPROXY_SECRET_BACKEND=file go test ./internal/server ./internal/translate ./internal/gateway -count=1
go test ./internal/gateway -run '^$' -bench BenchmarkChatPassThrough -benchmem -benchtime=1x
```

## Task 02: shared attempt coordinator

Files to add:

- `internal/requestmeta/request.go`, `request_test.go`
- `internal/gateway/attempt.go`, `attempt_test.go`
- `internal/config/engine.go`, `engine_test.go`

Files to modify:

- `internal/gateway/gateway.go`
- `internal/config/config.go`
- `internal/adapter/adapter.go`
- `internal/server/server.go`

Red-green-refactor behavior:

1. `TestAttemptBudgetAcrossAccounts` fails because current per-account retry loops can exceed
   a configured total attempt limit.
2. Implement a small coordinator with default total max attempts of three and a request-wide
   deadline, preserving the existing account order.
3. `TestAmbiguousSideEffectRequestNotReplayed` fails because request delivery certainty is not
   represented; add typed outcome metadata and prohibit an unsafe replay.
4. Migrate Chat, ChatStream, Responses and Claude request paths to the coordinator without
   altering native dispatch or aliases. Images keep their endpoint-specific no-replay behavior.

Proof: `go test ./internal/requestmeta ./internal/config ./internal/gateway ./internal/server -count=1`.

## Completion gates

At every slice: inspect diff, run focused tests, then run `PEAPROXY_SECRET_BACKEND=file go test ./...`.
Before push: review intent, run the repository security workflow based on changed paths,
scan secrets, build, run release checks, and request explicit permission before publishing.
When shipped or dropped, move this present-tense plan to `docs/archive/plans/`.

## Stamp

Verified earlier: full `go test ./...`, focused race, vet, and `node scripts/v2-ui-smoke.mjs`. These boxes are the plan deliverables still open. Do not treat a checked box as done until its proof command has been run.

- [x] Parse Retry-After on adapter HTTP errors and prove the cooldown uses it
- [x] Reject unrepresentable built-in tools instead of dropping them
- [x] Keep continuation bindings ahead of automatic route scoring
- [x] Drop cached responses when their account is removed
- [x] Record adaptive account measurements used for ordering
- [x] Add the plan's missing regression names for eligibility, continuation, transport, and exact-model routing
- [x] Add the end-to-end fixture and the remaining doc touches from task 16
- [x] Wire eligible in-flight response coalescing into the gateway, not only the cache package
- [x] Enrich live OpenRouter prices without inventing model ids
- [x] Add the locked Playwright smoke dependency from task 14
- [x] Run the task 16 fuzz, full-race, and benchmark commands and record the result
- [x] Record a response-cache hit as zero new upstream usage, separate from the cached body's published usage
- [x] Give a loading local model a longer stream prelude without changing the request deadline
- [x] Parse Retry-After on OAuth inference errors that still build HTTPError by hand
