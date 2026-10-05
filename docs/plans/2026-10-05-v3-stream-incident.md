# v3 release blocker: stream ended without finish_reason

Status: user-observed runtime failure; root cause unverified.
Reported during the v3 planning session on 2026-10-05, repeatedly.
Exact error: `stream ended without finish_reason`.
The user subsequently reported a recurring retry failure while large plan-writing responses were attempted. Its exact error text and relation to the missing terminal event remain unverified. Write planning artifacts in small sections to reduce interruption risk; this is a workflow mitigation, not a proxy fix.
Parent: [v3 implementation plan](2026-10-05-v3.md).

The client is reported to be using PeaProxy. The selected upstream, native wire, translated wire, request ID and installed proxy version have not yet been established. Do not attribute this to a specific adapter without tracing the request. Do not restart the proxy supporting the active session or enable raw prompt logging without permission.

## Investigation and acceptance

1. Obtain the installed version, selected model/provider, client version, approximate failure timestamps and sanitized request identifiers. Collect existing redacted diagnostics; do not publish credentials or conversation bodies.
2. Trace upstream termination through `internal/adapter/`, `internal/translate/sse.go`, `internal/translate/sse_tooluse.go`, `internal/gateway/wire.go`, `internal/gateway/gateway.go`, `internal/streamguard/guard.go` and the HTTP writer in `internal/server/server.go`.
3. Distinguish upstream EOF/reset, deadline/cancellation, incomplete tool-call deltas, translation dropping a legitimate terminal event, and client parser incompatibility. Check whether terminal bytes are flushed and errors are surfaced on the correct wire.
4. Capture a minimal sanitized fixture and add a failing regression test in `internal/gateway/stream_failure_test.go` or a new `internal/gateway/stream_terminal_test.go`, plus the relevant translator/adapter test. Reproduce the actual path rather than adding a fabricated successful finish.
5. A successful Chat Completions stream must deliver the appropriate finish_reason before [DONE]. Failed/truncated transport must not be converted into successful completion. Once committed, never replay on another provider; surface the protocol-appropriate failure. Legitimate length-limited completions retain their actual termination reason.
6. Verify with `PEAPROXY_SECRET_BACKEND=file go test ./internal/translate ./internal/streamguard ./internal/gateway ./internal/server`, then replay with the affected client through an isolated proxy instance. Automated fixtures alone do not establish that the live incident is fixed.

Include this investigation before default-changing optimizations in the v3 implementation plan. No fix has been implemented or verified.
