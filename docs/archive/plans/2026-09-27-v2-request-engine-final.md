# PeaProxy v2.0.0 — request-engine implementation plan

Status: **implemented.** Shipped in v2.0.10; the living document is [../../V2.md](../../V2.md).
Kept as the record of what was planned. The earlier approved draft is
[2026-09-27-v2-request-engine.md](2026-09-27-v2-request-engine.md) — this file supersedes it.
Baseline inspected: `2847896` (2026-09-27). Recheck the branch before implementation.

## 1. Goal and product boundary

Connect existing harnesses to connected providers with minimal setup. Improve request
compatibility, reliability, latency and eligible caching without requiring new workflows.
Success is an existing harness session working better, not a user learning a new platform.

Deliver automatic harness integration, high-fidelity translation, robust streaming,
adaptive account selection, credential coordination, provider prompt-cache optimization,
selective exact caching, local-runtime readiness and optional automatic model routes.

Workspaces, fleet orchestration, durable jobs, tool execution, knowledge stores, workflow
editors and financial dashboards are outside this release. Automatic summarization,
semantic response caching, tool pruning and reasoning reduction are experimental follow-on
work, not silent defaults. Amp management/WebSocket APIs are not added by this plan.

## 2. Architecture and constraints

Keep Go, Cobra, existing YAML configuration, secretstore, localhost HTTP server and embedded
browser UI. Prefer standard-library implementation; review new dependencies per slice.
No Redis, database server, paid cloud service or inference classifier is required.

Request path:

1. Read bounded request; establish request/session identity and incoming wire.
2. Inspect requirements without replacing the original JSON representation.
3. Resolve exact model/alias or explicitly selected automatic route.
4. Filter by capability/state constraints; select an account and acquire admission lease.
5. Obtain current credentials; choose native protocol or faithful translation.
6. Apply supported cache directives and narrowly scoped compatibility adaptations.
7. Execute under a total attempt/deadline budget; validate stream before commitment.
8. Normalize only required response fields; release leases; record bounded metadata.

### Invariants

- Preserve exact-model intent. Existing aliases and route-name response echo remain valid.
- Live provider discovery remains the source of model IDs. Metadata enriches, never creates,
  an allowlist or fictitious availability.
- Unknown capabilities/prices/quotas remain unknown. Unknown price never qualifies as free.
- Preserve raw bodies and opaque reasoning data on native paths. Use `jsonx` surgical edits;
  do not round-trip Anthropic/Codex bodies through `map[string]any`.
- Do not remove meaningful unsupported options or weaken JSON schemas to obtain HTTP 200.
  Reject unrepresentable translation with a protocol-appropriate actionable error.
- No generated-content splicing after stream commitment. No replay of ambiguous,
  side-effect-capable upstream requests merely because no client bytes were emitted.
- Cancellation releases queues, transport bodies, timers and concurrency leases.
- Provider credentials never reach harness configuration. Prompt logging remains opt-in.
- Cross-platform paths use Go home/config-directory APIs and environment overrides;
  never embed developer home directories.
- Existing LAN/admin controls apply to new endpoints. Configuration-writing admin actions
  require same-origin checks and bounded, enumerated client identifiers, not arbitrary paths.
- Changes are individually reviewable. Execute serially by default; parallel-safe labels
  describe file isolation, not authorization to start agents.

### Configuration policy

Keep schema version 1 for additive fields. Preserve `routes: map[string]string`.
Add separate typed `automaticRoutes` and `requestEngine` sections; do not reinterpret old
fields. Existing explicit failover policies retain their behavior; add `adaptive` as an
explicit policy and recommend it during new installation setup after verification.
Defaults enable fidelity/cancellation fixes; content-changing optimizations remain off.
Memory response caching starts opt-in; provider cache pass-through remains enabled.

### Shared contracts (implement in task 02)

- `internal/requestmeta`: `Wire` (chat/messages/responses/embeddings/images),
  `Requirements` (tools, parallel tools, strict schema, vision, stateful continuation),
  `Request` (internal ID, session ID, requested model, wire, requirements, deadline),
  `Attempt` (account, actual model, outcome, duration, commitment state, cache counters).
  Original body bytes stay in the existing gateway call arguments, not persistent metadata.
- `internal/catalog`: model metadata uses supported/unsupported/unknown values, source,
  observation time and optional expiry. Pricing distinguishes input/output/cache read/write
  and currency/unit; missing price is not zero.
- `adapter.HTTPError`: extend with parsed retry-after, failure scope and request-delivery
  certainty. Keep existing `Status`/`Body` callers working; sanitize before telemetry.
- `internal/gateway`: one attempt coordinator owns deadline/attempt accounting and records
  state transitions. Protocol-specific invocation remains a callback in gateway code.
- Stream commitment means the first validated event has been forwarded to the client.
  Internal prelude buffering does not imply a request is replay-safe.
- Stateful continuation IDs are bound to endpoint/account/model with bounded TTL. Unknown
  IDs may use only the explicitly addressed native account; never fabricate prior history
  or fail over to an unrelated account.

## 3. File and responsibility map

Existing files to extend:

| Area | Files | Responsibility |
|---|---|---|
| Gateway | `internal/gateway/gateway.go`, `wire.go` | Native/translated dispatch, aliases, account instances, affinity |
| HTTP boundary | `internal/server/server.go` | Incoming wires, stream headers, cancellation, admin endpoints |
| Adapters | `internal/adapter/adapter.go`, `httpclient.go` | Contracts, upstream observations, transport |
| Native paths | `internal/adapter/anthropic/anthropic.go`, `openai/openai.go`, `openai_compat/openai_compat.go`, `openai_oauth/openai_oauth.go`, `anthropic_oauth/anthropic_oauth.go` | Provider-specific request behavior |
| Translation | `internal/translate/claude.go`, `responses.go`, `sse.go`, `bridge.go`, `opaque.go`, `thinking.go` | Wire fidelity and opaque state |
| Raw JSON | `internal/jsonx/jsonx.go` | Minimal edits preserving unrelated request bytes |
| Routing | `internal/router/router.go`, `classify.go`, `policy.go`, `session.go` | Eligibility, failure classes, ordering, session identity |
| Provider data | `internal/catalog/catalog.go`, `internal/quota/parse.go`, `store.go` | Live inventory and observed limits |
| OAuth | `internal/oauth/token.go`, provider adapter files | Refresh semantics and persistence |
| Config | `internal/config/config.go`, `config_test.go` | Additive settings and validation |
| Harnesses | `internal/clients/clients.go`, `verify.go`, `internal/cli/root.go` | Existing presets, verification, command registration |
| UI | `internal/ui/web/app.js`, `index.html`, `styles.css` | Connect/select/repair experience |
| Usage | `internal/usage/usage.go`, `parse.go` | Bounded diagnostic counters, no accounting platform |

New files/packages are explicitly listed in tasks below. Tests stay adjacent to the owning
package. Use synthetic provider traffic in committed fixtures, never real credentials or
private conversation captures.

## 4. Dependency order and milestones

| Milestone | Tasks | Result |
|---|---|---|
| A: fidelity foundation | 01–04 | Characterized behavior, shared attempts, capability-aware translations |
| B: resilient requests | 05–08 | Stream commitment, quota-aware admission, coordinated credentials, transport lifecycle |
| C: invisible efficiency | 09–12 | Cache preservation, local readiness, optional automatic routes, eligible exact cache |
| D: effortless adoption | 13–15 | Connect/disconnect integrations, compact UI, compatibility-data updates |
| E: release candidate | 16 | End-to-end, performance, platform and release verification |

Serial integration order is the numbered order. Tasks 07, 10 and 13 contain parallel-safe
package work after their listed contracts land; shared gateway/server/config edits remain
serial. Do not combine all milestones into one review.

## 5. Implementation tasks

Every behavior task uses red → green → refactor: add the described failing regression,
run the named command to confirm the expected failure, implement the smallest coherent
change, rerun, then refactor under the same tests. Test names below are deliverables to
create, not claims that tests currently exist. No live credentials are required for them.

### 01 — Characterize the supported wire paths

Depends: none. Serial baseline; characterization is expected to pass before refactoring.

Files: add `internal/server/conformance_test.go`, `internal/gateway/benchmark_test.go`,
`internal/server/testdata/v2-chat.json`, `v2-messages.json`, `v2-responses.json`;
extend `internal/translate/opaque_test.go`, `internal/gateway/wire_test.go`.

- Cover all three chat wires, stream/nonstream, tools, vision, reasoning, aliases,
  nonretryable failures, cancellation and existing embeddings/image forwarding.
- Record upstream body and downstream events with `httptest` fake providers.
- Lock down the recent Codex multi-tool argument-delta regression and native body ordering.
- Assert errors do not silently become successful empty answers.
- Output: reusable fixture harness and compatibility baseline for all later tasks.
- Add native/translated fake-provider benchmarks for 1 KiB/100 KiB/1 MiB bodies and
  1/16/64 concurrency now, so task 16 compares against a measured pre-change baseline.

Proof: `PEAPROXY_SECRET_BACKEND=file go test ./internal/server ./internal/translate ./internal/gateway -count=1`.
Capture baseline separately: `PEAPROXY_SECRET_BACKEND=file go test ./...` and `go build ./...`.
Capture performance: `go test ./internal/gateway -run '^$' -bench . -benchmem -count=5`.
Any baseline failure is investigated before implementation; this planning session has not run it.

### 02 — Add request contracts and unify attempt accounting

Depends: 01. Serial.

Files: add `internal/requestmeta/request.go`, `request_test.go`;
`internal/gateway/attempt.go`, `attempt_test.go`;
`internal/config/engine.go`, `engine_test.go`;
modify `internal/gateway/gateway.go`, `internal/config/config.go`,
`internal/adapter/adapter.go`, `internal/server/server.go`.

- Implement contracts in section 2 and additive settings.
- Replace duplicated chat/messages/responses attempt loops with a shared coordinator;
  preserve native dispatch and existing explicit policy order.
- Enforce a total max-attempt count across accounts and a total request deadline.
- Track delivery uncertainty separately from retryable error classification.
- Image mutations never inherit chat retry rules; retain explicit endpoint-specific policy.
- Output: one request context and one attempt budget used by tasks 03–12.

Red: `TestAttemptBudgetAcrossAccounts` observes more upstream calls than configured;
`TestAmbiguousSideEffectRequestNotReplayed` detects unsafe retry.
Proof: `go test ./internal/requestmeta ./internal/config ./internal/gateway ./internal/server -count=1`.

### 03 — Introduce evidenced capabilities and request requirements

Depends: 02. Serial contract extension.

Files: add `internal/catalog/capabilities.go`, `capabilities_test.go`;
`internal/requestmeta/requirements.go`, `requirements_test.go`;
`internal/gateway/eligibility.go`, `eligibility_test.go`;
modify `internal/catalog/catalog.go`, `internal/adapter/adapter.go`.

- Inspect tool, vision, strict-schema and continuation requirements without rewriting JSON.
- Merge live model metadata with adapter protocol capabilities; preserve provenance/expiry.
- Unknown capabilities do not reject ordinary exact-model native pass-through; known
  unsupported features reject incompatible translation before sending upstream.
- Automatic routes require positive evidence for requested features.
- Output: eligibility decisions and reason codes; no network probes on the request hot path.

Red: `TestEligibilityUnknownIsNotSupported` and `TestExactNativeUnknownPassesThrough`.
Proof: `go test ./internal/catalog ./internal/requestmeta ./internal/gateway -count=1`.

### 04 — Close translation gaps and pin continuation state

Depends: 03. Serial.

Files: modify `internal/translate/claude.go`, `responses.go`, `bridge.go`, `opaque.go`,
`internal/gateway/gateway.go`, `wire.go`, `internal/jsonx/jsonx.go`;
add `internal/translate/fidelity_test.go`, `internal/gateway/continuation.go`,
`continuation_test.go`; extend existing translation golden tests.

- Native Messages/Responses paths take precedence when they support the request.
- Preserve tool IDs, parallel call ordering, opaque reasoning, cache fields and strict schema.
- Map equivalent token/reasoning parameters only through explicit provider capabilities.
- Reject unsupported built-in tools/stateful translation rather than dropping fields.
- Bind returned continuation IDs to account/endpoint/model; subsequent references restrict
  eligibility. Bound entries by count and TTL; account removal invalidates bindings.
- Output: faithful transforms and account-pinned state for the stream/retry layer.

Red: `TestFidelityStrictSchemaNeverWeakened`, `TestContinuationCannotCrossAccount`,
`TestNativePreservesOpaqueReasoning` exercise missing fidelity or binding behavior.
Proof: `go test ./internal/translate ./internal/jsonx ./internal/gateway ./internal/adapter/... -count=1`.

### 05 — Implement bounded stream prelude validation

Depends: 02–04. Serial.

Files: add `internal/streamguard/guard.go`, `guard_test.go`, `fuzz_test.go`;
`internal/gateway/stream_test.go`; modify `internal/translate/sse.go`,
`internal/gateway/gateway.go`, `wire.go`, `internal/server/server.go`.

- Parse incremental SSE framing across split UTF-8, CRLF, multiline data and comments.
- Buffer only until the first valid wire-specific lifecycle event; commit immediately then.
- Bound prelude to 64 KiB and 5 seconds by default; overflow or timeout is an explicit error,
  not an unvalidated flush. Settings may raise limits for a verified provider profile.
- In-band errors before commitment are eligible for retry only under task 02 replay rules.
- Errors after commitment terminate/report in the correct wire without starting another model.
- Distinguish first-event, idle and overall deadlines. Valid keepalives count as activity.
- Preserve final usage and cancellation. Backpressure cannot allocate unbounded buffers.

Red: `TestPreludeErrorBeforeCommit`, `TestNoRetryAfterCommit`,
`TestSplitToolArgumentsPreserved`, `TestPreludeBounded`.
Proof: `go test -race ./internal/streamguard ./internal/translate ./internal/gateway ./internal/server`.
Fuzz proof: `go test ./internal/streamguard -fuzz=FuzzSSE -fuzztime=30s`.

### 06 — Scoped cooldowns, admission and adaptive account routing

Depends: 02, 03, 05. Serial.

Files: add `internal/router/admission.go`, `admission_test.go`, `adaptive.go`,
`adaptive_test.go`; extend `internal/router/classify.go`, `policy.go`,
`internal/quota/parse.go`, `store.go`, `internal/gateway/gateway.go`,
`internal/adapter/httpclient.go`, `internal/config/engine.go`.

- Parse retry-after delta/date and documented reset headers with clamped durations.
- Scope cooldowns by account/model/endpoint only when observations justify that scope.
- Add bounded cancellation-aware admission queues and concurrency leases.
- Use measured in-flight count, latency/error EWMA and observed quota in adaptive ordering.
- Retain eligible session binding before optimizing order; half-open probes prevent storms.
- Default total attempt cap: 3; max admission wait: 2 seconds; max queued requests per
  account: 32. Per-account concurrency is profile/config based, not invented from tier.
- Unknown quotas retain ordinary eligibility, never imply unlimited remaining capacity.
- Output: selected account plus released-once lease; retry budget remains owned by task 02.

Red: `TestModelCooldownDoesNotDisableOtherModels`, `TestAdmissionCancelReleasesLease`,
`TestRetryAfterRespected`, `TestHalfOpenSingleProbe` using a fake clock.
Proof: `go test -race ./internal/router ./internal/quota ./internal/gateway ./internal/adapter`.

### 07 — Coordinate credential refresh and rotation

Depends: 02. Parallel-safe coordinator development; adapter integration serial.

Files: add `internal/oauth/refresh.go`, `refresh_test.go`;
modify `internal/adapter/anthropic_oauth/anthropic_oauth.go`,
`openai_oauth/openai_oauth.go`, `antigravity/antigravity.go`,
`xai_oauth/xai_oauth.go`, `kimi_oauth/kimi_oauth.go`,
`copilot_oauth/copilot_oauth.go`, and their adjacent tests.

- Account-scoped single refresh flight, immutable token snapshots, cancellation-aware waiters.
- Preserve provider-specific exchanges and omitted-refresh-token behavior.
- Prevent older refresh results overwriting a newer login using credential generations.
- Serialize persistence for the same account; propagate failure without logging tokens or
  rolling back to an already consumed rotating refresh token.
- Keep provider refresh skew behavior; network calls do not hold global gateway locks.
- Output: valid credential snapshot or typed auth error, with at most one concurrent refresh.

Red: `TestRefreshSingleFlight`, `TestLateRefreshCannotOverwriteLogin`,
`TestRefreshPersistenceFailureKeepsNewestRuntimeToken`.
Proof: `go test -race ./internal/oauth ./internal/adapter/... ./internal/secretstore`.

### 08 — Transport lifecycle and cancellation

Depends: 05, 07. Serial.

Files: modify `internal/adapter/httpclient.go`, `httpclient_test.go`;
add `internal/adapter/transport_test.go`; modify
`internal/adapter/openai_compat/openai_compat.go`, `anthropic/anthropic.go`,
`openai_oauth/openai_oauth.go`, `anthropic_oauth/anthropic_oauth.go`,
`internal/server/server.go`.

- Reuse owned transports with bounded idle pools and explicit header/dial timeouts.
- Separate stream idle tracking from blanket HTTP client total timeout.
- Ensure every response body closes, including parse failures and abandoned attempts.
- Cancel upstream work on downstream disconnect; no detached generation goroutines.
- Preserve proxy environment and TLS behavior; never disable verification for compatibility.
- Output: stable transport resource usage for cache/local/routing milestones.

Red: `TestDisconnectCancelsUpstream`, `TestLongActiveStreamSurvives`,
`TestFailedAttemptClosesBody`, measured with controlled fake transports.
Proof: `go test -race ./internal/adapter/... ./internal/server ./internal/gateway`.

### 09 — Provider prompt-cache preservation and supported optimization

Depends: 03–08. Serial.

Files: add `internal/promptcache/policy.go`, `policy_test.go`;
`internal/gateway/promptcache_test.go`; modify `internal/jsonx/jsonx.go`,
`internal/usage/parse.go`, `usage.go`, `internal/config/engine.go`,
`internal/adapter/anthropic/anthropic.go`, `openai_compat/openai_compat.go`,
`internal/gateway/gateway.go`.

- Normalize cache-read/write/uncached counters without double-counting provider totals.
- Preserve caller directives byte-for-byte outside necessary model/stream changes.
- Add explicit modes `preserve` (default), `optimize`, `off`; off disables PeaProxy-added
  directives, not caller-owned provider settings.
- Optimize only documented model/endpoint profiles; honor breakpoint limits and minimums.
- Stable account/endpoint-scoped cache hints use keyed hashes, not raw prompt fragments.
- Never reorder messages/tools or insert padding for caching. No background billable prewarm.
- Evaluate configured optimization using observed reads/writes; unknown savings stay unknown.
- Output: per-attempt cache counters and narrowly scoped request edits consumed by task 11.

Red: `TestCacheCountersProviderSemantics`, `TestPreserveCallerBreakpoints`,
`TestUnknownProfileAddsNoDirectives`, `TestOptimizeHonorsBreakpointLimit`.
Proof: `go test ./internal/promptcache ./internal/usage ./internal/jsonx ./internal/gateway ./internal/adapter/...`.

### 10 — Local endpoint readiness and cold-start handling

Depends: 03, 06, 08. Parallel-safe runtime package; gateway wiring serial.

Files: add `internal/localruntime/runtime.go`, `ollama.go`, `runtime_test.go`;
modify `internal/adapter/ollama/ollama.go`, `internal/adapters/presets.go`,
`internal/gateway/gateway.go`, `internal/config/engine.go`.

- Probe configured endpoints and a bounded list of known loopback service ports during
  setup/refresh, never scan a LAN or execute downloaded commands.
- Model readiness states: unknown/loading/ready/busy/offline with observed timestamp.
- Implement native Ollama readiness/load behavior where documented; generic OpenAI servers
  use discovery/health and report unknown residency rather than guessing.
- Queue local calls within task 06 limits; profile permits a longer first-event cold-start
  deadline without weakening idle/overall cancellation limits.
- Exact local-model requests never spill to cloud. Automatic routes may do so only according
  to the route selected in task 11.
- Output: readiness snapshot usable by eligibility and setup; no process/fleet manager.

Red: `TestLoadingIsNotOffline`, `TestLocalExactNeverCloudFallback`,
`TestBusyQueueCancellation`, `TestUnknownRuntimeDoesNotClaimLoaded`.
Proof: `go test -race ./internal/localruntime ./internal/adapter/ollama ./internal/adapters ./internal/gateway`.

### 11 — Simple automatic routes with capability and cost eligibility

Depends: 03, 04, 06, 09, 10. Serial.

Files: add `internal/router/automatic.go`, `automatic_test.go`;
`internal/catalog/pricing.go`, `pricing_test.go`;
`internal/config/routes.go`, `routes_test.go`;
modify `internal/adapter/openrouter/openrouter.go`, `internal/catalog/catalog.go`,
`internal/gateway/gateway.go`, `wire.go`.

- Add explicit routes `pea/auto`, `pea/economy`, `pea/local`, `pea/free` only when enabled;
  existing alias names cannot collide. Ordinary exact models retain existing semantics.
- `auto`: configured candidate preference, then readiness/observed reliability; no claim of
  objectively best reasoning. `economy`: comparable known estimated request price among
  eligible configured candidates. Unknown pricing follows known-priced options, never wins
  as zero. `local`: prefer configured local candidates with explicitly enabled cloud fallback.
  `free`: fresh verified zero API price only; subscription/local labels are insufficient.
- Enrich live OpenRouter pricing where provided; permit explicit versioned price overrides.
  No new IDs are introduced by a price table.
- Account for known cached-input rates only when reuse is evidenced; include output limits
  in estimates and label uncertainty. No token-padding or model-judge call on the hot path.
- Bind chosen model/account to the session. Stateful continuation takes precedence over
  route scoring. Candidate capability mismatch fails before upstream execution.
- Output: actual model/account selection and stable client-facing route name.

Red: `TestFreeExcludesUnknownPrice`, `TestExactModelNeverSubstituted`,
`TestAutomaticSessionStable`, `TestRequiredToolsFilterCandidates`,
`TestRouteAliasCollisionRejected`.
Proof: `go test ./internal/router ./internal/catalog ./internal/config ./internal/gateway ./internal/adapter/openrouter`.

### 12 — Bounded exact cache and explicit in-flight reuse

Depends: 02, 03, 08, 11. Serial.

Files: add `internal/responsecache/cache.go`, `key.go`, `flight.go`,
`cache_test.go`, `key_test.go`, `flight_test.go`;
`internal/gateway/cache_test.go`; modify `internal/gateway/gateway.go`,
`internal/config/engine.go`, `internal/usage/usage.go`.

- Start with opt-in embeddings caching and explicit stateless nonstream response caching.
  Memory-only default backend: 64 MiB total, 1 MiB entry cap, 5-minute TTL.
- Key account/endpoint/model revision/profile revision, wire and full semantically relevant
  request. Invalidate on account removal, route/profile changes and unknown model reload.
- Preserve order-sensitive arrays; cached byte buffers are immutable copies.
- Bypass tools, continuation IDs, mutable external URLs, images/edits and uncontrolled
  stochastic generations unless a supported integration explicitly requests reusable results.
- Coalesce only eligible calls; one cancelled waiter must not cancel other waiters. Last
  cancelled waiter cancels upstream. Failed/partial responses never enter the cache.
- Cache-hit accounting records zero new upstream usage, separate from original response usage.
- Output: upstream-call reduction for eligible repeats, without generic retry dedup guesses.

Red: `TestCacheAccountIsolation`, `TestCacheModelRevisionInvalidation`,
`TestCancelledWaiterDoesNotCancelPeers`, `TestToolsBypassCache`, `TestCacheMemoryBound`.
Proof: `go test -race ./internal/responsecache ./internal/gateway ./internal/usage`.

### 13 — Transactional harness connect/disconnect

Depends: 01, 02; final verification uses 04, 05, 11. Parallel-safe clients package work.

Files: add `internal/clients/discover.go`, `managed.go`, `managed_test.go`,
`opencode.go`, `codex.go`, `claude_code.go`, `continue.go`, `pi.go`;
`internal/cli/clients.go`, `clients_test.go`;
modify `internal/clients/clients.go`, `verify.go`, `internal/cli/root.go`;
add synthetic fixtures under `internal/clients/testdata/` for each managed format.

- First integration subtask verifies current official configuration documentation for each
  supported client/version; record URLs and tested versions in `docs/HARNESS.md`.
- Provide `clients detect`, `connect <name>`, `disconnect <name>`, `status`, existing `verify`.
- Use format-preserving edits for JSON/JSONC/TOML/YAML; select maintained parsers where
  necessary. If a client has no supported writable config surface, return guided setup
  through the existing preset rather than editing opaque application storage.
- Stage backup and atomic write; compare source fingerprint before applying. Track only
  owned keys. Disconnect restores owned keys without undoing unrelated subsequent edits.
- Runtime paths use OS APIs. Fixtures exercise macOS/Linux/Windows layouts, Unicode paths,
  symlink/conflict handling and existing user provider configuration.
- Never change the currently selected model/provider without the connect action selecting
  it. Synchronize only the PeaProxy-owned model entries.
- `verify` reports protocol probe versus actual installed-harness execution distinctly.
  Run bounded tool/stream verification against fixtures automatically; live inference is an
  explicit setup test and reports that it may consume provider quota.
- Output: transactional integration API used by task 14; no global shell-profile rewriting.

Red: `TestManagedConnectPreservesComments`, `TestDisconnectPreservesUserEdits`,
`TestConnectDetectsConcurrentEdit`, `TestClientPathsPortable`, `TestConnectIsIdempotent`.
Proof: `go test ./internal/clients ./internal/cli -count=1`.

### 14 — Connect/select/repair UI and bounded diagnostics

Depends: 06, 09–13. Serial.

Files: add `internal/server/clients.go`, `clients_test.go`, `engine.go`, `engine_test.go`;
modify `internal/server/server.go`, `internal/ui/web/app.js`, `index.html`, `styles.css`,
`internal/usage/usage.go`, `parse.go`, `internal/cli/health.go`;
add `scripts/v2-ui-smoke.mjs`, `scripts/package.json`, `scripts/package-lock.json`.

- Extend existing admin clients API with `POST /admin/clients/{name}/connect`,
  `POST /admin/clients/{name}/disconnect`, `POST /admin/clients/{name}/verify`;
  no arbitrary filesystem path parameters. Add `GET /admin/engine` for bounded status.
- Setup flow: connect provider → choose detected harness → choose exact/automatic model →
  verify. Show guided setup for unsupported automatic integrations.
- Surface actionable auth/capability/local-readiness errors; detail view explains fallback,
  attempts and cache state. Normal operation needs no policy editor or cost dashboard.
- Default telemetry stores reason codes/timings/counters only; redact upstream error bodies.
- Browser tests exercise setup, disconnect, failed verification, cancellation and rendering
  adversarial model/provider strings without script execution. Use mocked providers and
  a temporary HOME/config directory, never modify the developer's real client settings.
- Declare Playwright as a dev dependency for the smoke runner; use a locked install.

Red: `TestClientMutationRejectsForeignOrigin`, `TestClientMutationRejectsUnknownName`,
`TestEngineStatusRedactsSecrets`; UI smoke initially fails on missing connect controls.
Proof: `go test ./internal/server ./internal/clients ./internal/usage ./internal/cli`;
`npm ci --prefix scripts`; `node scripts/v2-ui-smoke.mjs` (runner starts/stops isolated fixture server).

### 15 — Versioned compatibility metadata with rollback

Depends: 03, 04, 09, 13. Serial; shipped last among features.

Files: add `internal/compatdata/schema.go`, `store.go`, `update.go`,
`schema_test.go`, `update_test.go`, `builtin.json`;
modify `internal/catalog/capabilities.go`, `internal/promptcache/policy.go`,
`internal/clients/clients.go`, `internal/config/engine.go`;
add `docs/COMPATIBILITY.md`.

- Data-only, schema-versioned profiles for capability facts, parameter equivalence,
  documented cache support and client-version matching. No script execution, arbitrary
  file edits or regex-based arbitrary body transformation downloaded as data.
- Bundle a known-good snapshot. Support explicit import/update with authenticated manifests,
  content hashes, size bounds, compatibility validation and atomic rollback.
- Use Ed25519 verification with a configured trusted publisher key; no embedded private key.
  Remote automatic update stays off until a release-published signed feed and key rotation
  procedure have passed the same tests. Bundled profiles require no network service.
- Metadata never invents discovered model IDs; unsupported/new profiles fall back to native
  pass-through or honest translation rejection.
- Output: reproducible profile version in request diagnostics; last-known-good data survives
  invalid updates and offline operation.

Red: `TestRejectUnsignedMetadata`, `TestIncompatibleSchemaKeepsLastGood`,
`TestRollbackAtomic`, `TestMetadataCannotCreateCatalogModels`.
Proof: `go test ./internal/compatdata ./internal/catalog ./internal/promptcache ./internal/clients`.

### 16 — Release qualification and migration documentation

Depends: all preceding tasks. Serial release gate.

Files: add `internal/server/v2_e2e_test.go`, `docs/V2.md`;
update `internal/gateway/benchmark_test.go`, `README.md`, `CONTRIBUTING.md`, `docs/CONFIG.md`,
`docs/HARNESS.md`, `docs/PROVIDERS.md`, `docs/RELEASING.md`, `docs/PLAN.md`,
`.github/workflows/ci.yml`; extend `internal/releasemeta/` checks when release behavior changes.

- End-to-end fixture scenarios: three wires with tool loops; 429 reset/failover; native
  continuation; pre/post-commit failures; refresh concurrency; local cold start; automatic
  route exactness; eligible cache reuse; managed connect/disconnect.
- Preserve image generation/edit and embeddings regression coverage throughout.
- CI test matrix: Linux, macOS, Windows; race on supported runners. WSL and archserver
  receive native release-candidate smoke runs, not merely cross-compilation claims.
- Test existing schema-1 configuration loading, secret references, explicit routing and
  rollback to the prior binary using an untouched backup configuration.
- Add local no-network benchmarks for 1 KiB/100 KiB/1 MiB bodies, 1/16/64 concurrency,
  native/translated streams and caching enabled/disabled. Compare against task 01 baseline
  on the same host/toolchain; preserve reports outside committed fixtures.
- Acceptance: no unexplained >10% median regression in native pass-through CPU time or
  allocation bytes at 1/16 concurrency; zero unbounded growth in a 10-minute cancellation/
  failure soak; stream prelude never exceeds configured cap. Investigate failures rather
  than relaxing thresholds automatically. No universal upstream latency/savings claims.
- Installed-harness release matrix records actual tested client versions and selected
  native/translated upstream paths. Fixture success alone is not advertised as live support.
- Update version-facing documentation at release time, not during early implementation.

Proof commands:

```sh
PEAPROXY_SECRET_BACKEND=file go test ./...
PEAPROXY_SECRET_BACKEND=file go test -race ./...
go vet ./...
go build ./...
go run ./cmd/peaproxy --help
go run ./cmd/peaproxy --version
go test ./internal/streamguard -fuzz=FuzzSSE -fuzztime=60s
go test ./internal/gateway -run '^$' -bench . -benchmem -count=5
node scripts/v2-ui-smoke.mjs
goreleaser check
git diff --check
```

## 6. Review, security and publication gates

Before each implementation slice: inspect working tree, establish isolated branch/worktree
according to the repository workflow, run relevant baseline tests, then follow the red/green
cycles above. Existing untracked `.opencode/` content is not part of this work.

Review each slice for raw-body preservation, truthful metadata, replay safety, bounded
resources, cancellation, cross-account isolation and configuration compatibility. Re-run
focused tests after review changes; full suite at milestone integration.

Before publishing, read `${GENV_ROOT:-$HOME/.config/genv}/agents/global/ops/security.md`
and select scanners from the actual changed paths. At minimum run `govulncheck ./...` for
Go/dependency changes and redacted secret scanning. If task 14 adds npm manifests, audit
that locked dependency tree. Run actionlint/zizmor when CI changes. Run the agent-surface
audit if client-integration changes fall within that policy's agent-surface scope. Container/
IaC scanners apply only if such paths are introduced. No paid cloud login gate.

Previously deferred workflow findings remain recorded as deferred; do not describe the
earlier conversational scanner summary as verified evidence. Fresh output governs triage.
Fix material new findings or ask for a specific waiver; do not silently waive them.

Publish only after explicit authorization: inspect status/diff/recent commits, commit
intended files only, open reviewed PRs, qualify release candidate, then authorize the v2.0.0
tag separately. No implementation-plan creation implies permission to tag/push/release.
When the plan ships or is dropped, move it to
`docs/archive/plans/2026-09-27-v2-request-engine.md` in the closing change.

## 7. Definition of done

- Existing harness workflows use the same public inference endpoints.
- Supported managed clients connect/disconnect without losing unrelated settings.
- Native tool/reasoning behavior survives; unrepresentable translations fail explicitly.
- Rate limiting, refresh and cancellation do not create storms or unbounded work.
- Streams preserve event/argument fidelity and never replay after commitment.
- Prompt-cache preservation is default; additional cache behavior is evidence-based.
- Free-only never routes to unknown-priced/paid models; exact-model never silently changes.
- Local cold starts are handled within bounded deadlines and explicit fallback choices.
- Cross-platform, end-to-end, performance and security gates have recorded fresh evidence.
- The ordinary UI asks users to connect and choose, not construct infrastructure policies.

## Stamp

Remaining work against this plan. A box is checked only after its proof command has run.

- [x] Write Continue 1.x config.yaml and leave unrelated YAML in place
- [x] Drop cached responses when a successful refresh removes that model
- [x] Benchmark native chat at 1, 16, and 64 concurrent calls
- [x] Load bundled compatibility profiles into automatic-route evidence and prompt-cache limits
- [x] Preserve existing JSON bytes when connecting OpenCode and Claude Code
- [x] Parse Retry-After on the remaining adapter responses that still build HTTPError by hand
- [x] Cover messages, responses, 429 failover, and pre-commit stream failure in the end-to-end fixture
- [x] Keep a cancellation soak from growing goroutines, including a 10-minute run
- [x] Run the Playwright smoke in CI
- [x] Record that installed harness versions were not live-tested
- [x] Return guided setup for Pi instead of writing a config file
