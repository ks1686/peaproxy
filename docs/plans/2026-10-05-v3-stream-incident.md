# v3 release blocker: stream ended without finish_reason

Status: user-observed runtime failure; **PeaProxy side ruled out by exhaustive
test** (2026-10-05). Remaining cause is upstream or client-side.

## Investigation result

Run entirely against local mock upstreams with no credentials, after a live
probe against the running proxy was ruled out: the proxy is shared state, a
failed request mutes accounts through the cooldown, and probing it that way
affects the session using it. Every test below costs nothing and touches no
account.

**The live proxy could not reproduce the failure.** Short and long streams on
both wires terminated correctly:

- `/v1/messages` (Claude wire) emitted `message_delta` with `stop_reason`
  followed by `message_stop`.
- `/v1/chat/completions` emitted `finish_reason` followed by `[DONE]`.
- Three ~50 s, ~150 KB streams all terminated correctly, so neither duration
  nor size is a factor.

**PeaProxy cannot manufacture this failure.** `routeRewriter` was tested at
*every byte split point* of a well-formed stream: the client-visible output is
byte-identical to the upstream modulo the model rewrite. The `openai_compat`
family, which serves the free-tier providers, was tested the same way: a
complete upstream stream delivers its `finish_reason` and `[DONE]` regardless of
where the network splits it, and a genuinely truncated upstream is forwarded as
truncated with no sentinel fabricated.

So if D1 was seen on a passthrough wire, the upstream stream itself lacked the
terminal event. PeaProxy forwarded what it was given.

### One real defect found and fixed

`routeRewriter` holds a partial trailing token in case a model name continues in
the next write. Nothing drained that hold at end of stream, so a stream stopping
mid-token lost its tail silently. This is **not** the D1 cause -- the loss only
occurs on an already-truncated stream -- but it hid how far a stream actually
got, which is precisely what makes this class of failure hard to diagnose.
`Flush` now drains it on all three stream paths.

### What is still needed

The remaining cause is not in this repository. To close it:

1. The failing request's provider and account (the free-tier OpenAI-compatible
   provider is the prime suspect; the translated Claude wire is ruled out).
2. The client and its version, since "stream ended without finish_reason" is
   client-side wording and some clients raise it where others do not.
3. The bounded SSE tail, which requires the diagnostics build deployed. The
   running instance predates it and its log had rotated past the incident.

Deploying the diagnostics build to a non-session instance and replaying the
affected client is the confirming step. Automated fixtures cannot stand in for
it: they can prove PeaProxy is innocent, which is now done, but they cannot
establish what the provider did.
Reported during the v3 planning session on 2026-10-05, repeatedly.
Exact error: `stream ended without finish_reason`.
The user subsequently reported a recurring retry failure while large plan-writing responses were attempted. Its exact error text and relation to the missing terminal event remain unverified. Write planning artifacts in small sections to reduce interruption risk; this is a workflow mitigation, not a proxy fix.
Parent: [v3 implementation plan](2026-10-05-v3.md).

The client is reported to be using PeaProxy. The selected upstream, native wire, translated wire, request ID and installed proxy version have not yet been established. Do not attribute this to a specific adapter without tracing the request. Do not restart the proxy supporting the active session or enable raw prompt logging without permission.

## New evidence found while adding diagnostics

The request log now records `streamTerminal`, so the two wire families can be told apart:

- **Native passthrough** forwards the provider's bytes unchanged. A provider that ends without a terminal event leaves the client with none, which is the reported error.
- **Translated wires** (Claude and Responses produced by PeaProxy) treat a clean upstream EOF as a finished turn and emit `message_stop` or `response.completed`. This is deliberate and documented in `internal/translate/sse.go`, on the reasoning that a connection cut mid-body arrives as a read error rather than a clean EOF.

The gap that reasoning leaves: an intermediary which closes a response cleanly part-way through a long turn is indistinguishable from a provider that finished, so on the translated wires a truncated answer is reported as complete, while on the passthrough wire the same upstream produces the reported missing terminal event. Both behaviours are pinned by tests so a change to either is deliberate. Which one produced the reported incident is still unknown; that needs an observed failing request.

## Investigation and acceptance

1. Obtain the installed version, selected model/provider, client version, approximate failure timestamps and sanitized request identifiers. Collect existing redacted diagnostics; do not publish credentials or conversation bodies.
2. Trace upstream termination through `internal/adapter/`, `internal/translate/sse.go`, `internal/translate/sse_tooluse.go`, `internal/gateway/wire.go`, `internal/gateway/gateway.go`, `internal/streamguard/guard.go` and the HTTP writer in `internal/server/server.go`.
3. Distinguish upstream EOF/reset, deadline/cancellation, incomplete tool-call deltas, translation dropping a legitimate terminal event, and client parser incompatibility. Check whether terminal bytes are flushed and errors are surfaced on the correct wire.
4. Capture a minimal sanitized fixture and add a failing regression test in `internal/gateway/stream_failure_test.go` or a new `internal/gateway/stream_terminal_test.go`, plus the relevant translator/adapter test. Reproduce the actual path rather than adding a fabricated successful finish.
5. A successful Chat Completions stream must deliver the appropriate finish_reason before [DONE]. Failed/truncated transport must not be converted into successful completion. Once committed, never replay on another provider; surface the protocol-appropriate failure. Legitimate length-limited completions retain their actual termination reason.
6. Verify with `PEAPROXY_SECRET_BACKEND=file go test ./internal/translate ./internal/streamguard ./internal/gateway ./internal/server`, then replay with the affected client through an isolated proxy instance. Automated fixtures alone do not establish that the live incident is fixed.

Include this investigation before default-changing optimizations in the v3 implementation plan. No fix has been implemented or verified.
