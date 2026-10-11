# Design: a durable, classifiable error log

Date: 2026-10-11
Status: approved by Karim
Slice: 2 of `active-01a1283c` (stacked on PR #146)

## Problem

PeaProxy cannot currently answer "why did that request fail, and which account
was affected". Two stores hold failures, and both age them out at the same rate
as successes:

| Store | Cap | Measured on this machine (2026-10-11) |
|---|---|---|
| `requests.log` | 1 MB, then keeps the last 80 events | 241 events, 10.8% errors; **37% of all bytes are prompt previews from successful calls** |
| `usage.json` | 200 events | 25 errors spanning **4.5 hours** |

Consequences:

1. **Failures are evicted by traffic, not by age.** A busy afternoon rotates out
   the 401 that explained this morning's breakage. Rotation keeps the last 80
   events regardless of outcome, so successes and failures compete for the same
   slots.
2. **Failures carry no classification.** `Error` is a free-text string. 25 of
   this machine's 26 current errors are the string `context canceled` — a client
   disconnect, not a fault. One real error (an upstream 404) sits in a file
   otherwise full of noise, and there is no way to tell them apart without
   reading every line.
3. **Nothing about authentication survives.** `auth` failures are recorded as
   whatever string the provider returned. There is no record of whether a token
   refresh was attempted, what it returned, or what the token's expiry was at the
   moment of failure. This is why the recurring "I have to log in again" on the
   xai and openai accounts has left no trace.

The immediate motivation is a diagnosis. The live verification on 2026-10-11
could show that failures happen but not why, which is a gap in the product and a
blocker on answering that question.

## Decision

Add a third store, `errors.jsonl`, holding one record per request that was not a
clean success, with time-based retention of 7 days and no prompt previews. "Not
a clean success" rather than "a failure", because two of the classes recorded
here are not faults: a client disconnect and an unmeasured call. Both belong in
this store because both are things worth being able to see, and separating them
by a flag is what lets the export count faults without hiding the rest. Add a
stable `class` to every event so failures are aggregatable. Add `peaproxy errors
export` to produce a redacted bundle for analysis.

### Retention is time-based, not size-based

Karim's decision. A size cap is the wrong instrument for an error stream: a
provider outage can produce thousands of failures in minutes and evict the
evidence of the outage that preceded it. A 7-day window bounds the file by age
instead. It is also short enough to stay relevant given how often the software
changes.

`requests.log` stays size-capped at 1 MB. It is a raw tail for live debugging,
not an archive.

### `usage.json` eviction is not changed

`usage.json` is an accounting ledger that drives the spend panel and the
ceiling. Biasing its retention toward errors would distort the accounting: the
denominator of "requests in the spend window" would no longer be every request.
The new error store is the right home for error durability; the ledger keeps
being a faithful chronological record.

### `client_cancel` is recorded, and is not a failure

25 of 26 current errors on this machine are client disconnects — an agent closing
a stream mid-response. They are real events worth seeing (a spike is itself a
signal: an agent crashing mid-stream) and they are *not* faults. They get their
own class, they are kept in the error store, and the export separates them from
failures rather than mixing them in.

## Data model

Every `usage.Event` gains:

```
class  string   // a FailureClass value; empty means "succeeded cleanly"
```

and events carrying diagnostics gain an optional detail object, redacted:

```
detail object   // present only for auth classes; see below
```

### Classes

| class | meaning | failure? |
|---|---|---|
| `client_cancel` | client disconnected before completion | no |
| `not_dispatched` | PeaProxy refused before any provider call | no |
| `unmeasured` | call succeeded but published no token counts | no |
| `auth_revoked` | 401 / `invalid_grant` / `authentication_error` | yes |
| `auth_refresh_failed` | a token refresh attempt failed | yes |
| `secret_unreadable` | a stored secret could not be read | yes |
| `rate_limit` | 429 or a provider quota body | yes |
| `entitlement` | 402 / billing / free-tier refusal | yes |
| `overloaded` | 503, 529, capacity body | yes |
| `upstream_error` | any other 4xx/5xx | yes |
| `not_found` | 404 / unknown model | yes |
| `stream_cut` | stream ended without a terminal event | yes |
| `timeout` | deadline exceeded | yes |
| `unknown` | a failure we could not classify | yes |

`unmeasured` is listed as a non-failure because a successful call is not a
failure, but it is recorded as a class because it is the state a spend ceiling
fails closed on, and it was the subject of PR #146. It belongs in the error
store so a session that is silently unmeasurable is visible.

### Classification lives in `internal/usage`, stdlib only

`internal/router.Classify` already exists and already matches `invalid_grant`.
It is not reused, because it answers a different question — "should this account
be cooled and another tried?" — and its value set is a failover decision, not a
diagnosis. Coupling the two would make either one worse.

The new classifier is a pure function of `(status int, err error)` using only the
standard library. `internal/usage` currently imports just `internal/fslock`, and
neither `adapter` nor `router` imports `usage`, so this introduces no cycle
either way; the choice is made on dependency direction, not cycle avoidance.

There is deliberate pattern overlap with `router`'s list. Two taxonomies that
answer different questions is better than one that answers both badly.

### Where it hooks

`recordEvent` in `internal/server/server.go` is the single place where account,
provider, model, protocol, path, status, stream, terminal event, error and
`NotDispatched` are all in scope. Classification happens there and writes
`e.Class`.

## The error store

- Path: `<config dir>/errors.jsonl`, mode `0600`, JSONL, append-only.
- One line per event that is not a clean success.
- **No prompt previews.** A failed request's prompt is normally something the
  user already knows they sent; copying it into a file meant to be uploaded is a
  privacy cost with no diagnostic benefit. Structured metadata only: account,
  adapter, model, protocol, path, status, duration, class, and the redacted
  upstream body.
- Retention: 7 days, enforced on append by dropping lines older than the cutoff.
  Lines are written in time order, so this is a cheap prefix scan.

### Redaction is a correctness requirement, not a formality

An upstream 401 body routinely echoes the offending token. `usage.Redact`
already exists and is applied to error strings; this design requires that
provider bodies pass through it before they reach a file whose whole purpose is
to be handed to someone else. A test asserts that a body containing a
recognisable token does not appear verbatim in the export.

## Export

```
peaproxy errors export --since 24h [--out path]
```

Emits a bundle:

```json
{
  "generated": "...",
  "since": "...",
  "summary": { "failures": 12, "cancels": 25, "byClass": {...}, "byAccount": {...} },
  "events": [ ... ]
}
```

Redacted, and with the failures/cancels split that `client_cancel` is retained
for. `--since` accepts `24h`, `7d`, or a date.

## Auth detail

The class that answers the recurring-log-out question:

```json
{ "class": "auth_refresh_failed",
  "detail": { "provider": "xai_oauth", "accountId": "xai-oauth",
              "refreshAttempted": true, "refreshError": "invalid_grant",
              "tokenExpiresAt": "2026-10-11T07:58:08Z" } }
```

`auth_revoked` carries `refreshAttempted` and whether a refresh had just failed,
so "the grant was revoked" and "the refresh was refused" are distinguishable.
No token material, only lengths and expiry, as the store already does elsewhere.

## Testing

- Taxonomy: one table case per class, each from a representative error.
- `client_cancel` and `not_dispatched` are recorded with their class and reach
  the store, but are counted as cancels, not failures.
- A body containing a recognisable token does not appear verbatim in the export.
- 7-day prune drops older lines and keeps newer ones.
- `errors.jsonl` is created `0600`.
- End-to-end through the server: a failing request lands in `errors.jsonl` with
  the right class and no preview.
- Mutation-verified, as in slice 1.

## Rejected

- **Bias `usage.json` retention toward errors.** Distorts the accounting
  denominator. See above.
- **A filter flag on `requestLog`** (`errorsOnly: true`). Turns one lossy stream
  into a differently lossy stream; you lose successes when you want them, and it
  is a breaking config change for a partial answer.
- **Size-based error retention.** Rejected above.
- **Exporting upstream bodies unredacted.** Defeats the purpose of a file meant
  to leave the machine.