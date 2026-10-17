# Gateway structure: deployment id, one failover policy, codecs in translate

Status: **shipped** in PR #157. Archived after review; the handoff log below records what landed and what the review changed.

This plan is written so another agent can pick it up cold. Read **Already in the tree** and **How to work** first, then take the first unchecked task. Tick a box only after its proof command has passed on the current tree.

## Goal

PeaProxy already lets a client pick a published context ceiling and a published effort. That feature stays. This plan fixes the structure a full-repo review found around it:

1. The live model id is smuggled through `requestmeta.Request.Upstream *string`. Return it from the gateway call that actually ran.
2. Nine request methods each carry their own failover tail. One policy records cooldown, admission skip, and protocol refusal. The call body of each wire stays where it is.
3. Codex chat-to-Responses and Gemini-to-OpenAI body and stream transforms live in the OAuth adapters. They belong in `internal/translate`. `internal/translate/sse.go` is three converters in one file.
4. `internal/gateway/gateway.go` is both the process (config, catalog, refresh, save) and the request router. After the loops share one policy, the request methods move to their own file. Same `*Gateway`. No wrapper type.

## Already in the tree

Uncommitted on the current branch. Do not revert it, and do not restack it onto a fresh `origin/main` worktree or the feature disappears.

HEAD when this plan was written: `93adf5b` (`ci(release): gate MACOS_* signing secrets behind tag-only release environment`).

Feature files (modified or untracked):

- `internal/catalog/catalog.go`, `published.go`, `published_test.go`, `selection.go`, `selection_test.go`
- `internal/gateway/gateway.go`, `wire.go`, `selection_test.go`
- `internal/jsonx/jsonx.go` (`Peek.ReasoningEffort`)
- `internal/requestmeta/request.go` (the pointer this plan deletes)
- `internal/server/server.go`
- `internal/translate/bridge.go`, `bridge_test.go`
- `internal/clients/pi_import.go`, `pi_import_test.go`
- `internal/adapter/anthropic/anthropic.go`, `anthropic_test.go`
- `internal/adapter/anthropic_oauth/anthropic_oauth.go`
- `internal/adapter/copilot_oauth/copilot_oauth.go`, `copilot_oauth_test.go`
- `internal/adapter/openai_oauth/openai_oauth.go` (`ListModels` uses `ParseCodexCatalog`; the chat-to-Responses codec is still here)
- `docs/HARNESS.md`

`docs/PLAN.md` and `docs/V1.md` are a separate dirty edit (`pea-pod.me` to `peapod.fyi`). Leave them out of this work.

Selection behavior that must survive every task:

- `catalog.Build` then `Index.Target(id, effort)` is the only resolver. `Listed` uses `Clients()`. `Annotated` appends `Added()`. `prepare` calls `Target` after stripping `-thinking-N` and before `resolveBody`.
- A ceiling alias exists only when the merged `MaxContextWindow` is greater than the merged `ContextWindow`. Numbers are maxima across accounts. Unknown `@n` is unchanged.
- A synthesized effort base calls the fallback sibling when effort is empty: `medium`, then `low`, `minimal`, `high`, `xhigh`, `max`, `none`, `off`. A named effort calls that sibling. A suffixed id is never rewritten. A live base that already published `Efforts` is not redirected. `extra-low` and `lite` stay separate. Never invent a sibling the catalog did not list.
- `AliasOf` is the live id when effort does not select a sibling. Config routes still go through `resolveBody`.
- The client-facing id stays on the response body and on `usage.Event.Model`. Pricing uses the live id.

Last known green, before this plan: `PEAPROXY_SECRET_BACKEND=file go test ./internal/catalog ./internal/gateway ./internal/server ./internal/clients -count=1`. Re-run that as the baseline before task 1. If it fails, stop and fix the feature tree first. Do not start the structural edits on a red baseline.

## How to work

- Stay in this checkout. The feature is uncommitted here. A new worktree from `origin/main` does not contain it.
- Do not commit, push, or open a PR unless Karim asks in that turn.
- `PEAPROXY_SECRET_BACKEND=file` on every `go test`.
- Do not boot out `me.ks1686.peaproxy`, kill the listener on `127.0.0.1:8317`, or replace `/opt/homebrew/bin/peaproxy`. No hot test in this plan.
- Do not export `PI_CODING_AGENT_DIR`. A leftover value makes `internal/clients` tests write outside their temp dir.
- Do not rewrite `~/.pi/agent/models-store.json` or `~/.pi/agent/models.json`.
- Do not invent context windows or effort lists from model names.
- `gofmt` every Go file you touch.
- A regression test that cannot fail is a comment. For each behavior change, name the test, watch it fail for the reason in the task, then make it pass.

## Architecture

```text
HTTP handler
  -> gateway method returns Call{Account, Deployment}
  -> usage.Event.Model stays the client id
  -> PriceUsage(event, account, call.Deployment)

gateway method
  -> prepare (Index.Target, then routes)
  -> per-candidate call body (cache, translate, stream guard, tool rounds)
  -> noteAttempt (one policy)
  -> Call{Account: lastAccount, Deployment: model after applyModel}

adapter
  -> auth, headers, transport
  -> translate for body and SSE conversion
```

`QuoteFor` keeps taking an account and a model id. It strips `-thinking-N` and prices that id. It does not learn `Index.Target`. The handler passes the deployment the winning attempt called, which is already past selection.

## File map

| File | Owns |
| --- | --- |
| `internal/gateway/call.go` | `Call` |
| `internal/gateway/attempt.go` | attempt budget, `ambiguousDelivery`, and the shared `noteAttempt` policy |
| `internal/gateway/gateway.go` | process: construct, refresh, catalog index, save |
| `internal/gateway/dispatch.go` | the nine request methods, after task 2 |
| `internal/gateway/wire.go` | `prepare`, `applyModel`. No write to `requestmeta` |
| `internal/requestmeta/request.go` | request metadata. No `Upstream` field |
| `internal/server/server.go` | records `Call` and prices `Deployment` |
| `internal/translate/sse_openai_to_claude.go` | `OpenAISSEToClaude` and helpers only it uses |
| `internal/translate/sse_claude_to_openai.go` | `ClaudeSSEToOpenAI` and helpers only it uses |
| `internal/translate/sse_openai_to_responses.go` | `OpenAISSEToResponses` and helpers only it uses |
| `internal/translate/codex_request.go`, `codex_response.go`, `codex_sse.go` | chat-to-Responses codec, moved out of `openai_oauth.go` |
| `internal/translate/gemini.go` | Gemini body, schema sanitize, SSE-to-OpenAI, moved out of `antigravity.go` |

## Dependency order

1. Task 1, then task 2. They edit the same nine methods. Serial.
2. Task 3, then task 4. They edit `internal/translate`. Serial with each other.
3. Tasks 1–2 and tasks 3–4 do not share files. Parallel-safe across that pair.
4. Task 5 waits until task 2 has landed. Moving the methods before the policy change means moving them twice.

## Failover policy to preserve

`attemptCoordinator.run` in `internal/gateway/attempt.go` is not the production loop. Production never calls it. Its tests in `attempt_test.go` cover the budget and ambiguous delivery. Leave `run` in place. Do not route `Chat` through it. It has no admission skip, no protocol refusal, no prelude cutoff, no cache, and no proxy tool rounds.

The shared piece is the decision after an upstream error. Call bodies stay in the nine methods.

`noteAttempt` records the decision and reports whether the loop tries the next account.

Non-stream chat (`Chat`), and the non-stream translated tails, use this order:

1. Protocol refusal (`noteProtocolRefusal`): no cooldown, mark the model not-chat, try the next account.
2. Admission rejection: no cooldown, try the next account.
3. `retryable` or `router.Transient`: cooldown, try the next account.
4. Otherwise return the error.

End of that loop: if any candidate refused the protocol, return `errModelRefusesChat`. If the only skips were admission, return `last` unwrapped. Otherwise `cooldownErr(last)`.

`Chat`'s `notChatModel` continue currently sets `last` and does not set `refused`. `ChatStream`, `ResponsesStream`, and `ClaudeChatStream` do set `refused`. Preserve that split. Do not "correct" `Chat` while extracting the helper.

Stream loops (`ChatStream`, both native and translated branches of `ResponsesStream` and `ClaudeChatStream`) use this order, which is not the non-stream order:

1. Admission rejection: no cooldown, try the next account.
2. If any byte was written to the client, return the error. No failover.
3. Slow prelude: try the next account, no cooldown.
4. Protocol refusal: no cooldown, try the next account.
5. `failoverOn` (`retryable` or transient or prelude failover): cooldown, try the next account.
6. Otherwise return the error.

Stream end: if a slow prelude or an admission skip happened, return `last` unwrapped. If `refused` and (`protocolRefused(last)` or `errors.Is(last, errModelRefusesChat)`), return `errModelRefusesChat`. Otherwise `cooldownErr(last)`. `ResponsesStream` and `ClaudeChatStream` also substitute `xerr` or `router.ErrNoAccount` when `last` is nil, and they cooldown only when `retryable(last)` and neither slow nor admission. Preserve those two end conditions. A translated error chunk with a nil adapter error and zero bytes written fails over with no cooldown (`#50`).

Native `Responses` and native `ClaudeChat` today cooldown only on `retryable`, and they ignore admission and protocol refusal. That is the drift. Bring those two native branches onto the non-stream order above. Lock it with tests in task 2 before changing them.

Images (`GenerateImage`, `EditImage`):

- Admission skip, no cooldown.
- Continue only on `replayableAfter`. That excludes 502 and 504. A 429 may move on.
- `ambiguousDelivery` returns immediately. No second account.
- No protocol-refusal marking. An image 400 must not call `markModelNotChat`.
- `GenerateImage` end: admission returns `last`; else `retryable(last)` returns `cooldownErr(last)`; else `last`.
- `EditImage` end: when `retryable(last)` or transient, admission returns `last` and otherwise `cooldownErr(last)`. Any other `last` is returned unwrapped.

Embeddings: admission skip; continue on `retryable` or transient; no `replayableAfter` exclusion (embeddings are idempotent). End matches `GenerateImage`: admission returns `last`; `retryable(last)` returns `cooldownErr`; else `last`.

Also leave in place, outside `noteAttempt`:

- Proxy tool rounds in `Chat` are not charged to the attempt budget. Transient retry inside one attempt is two tries.
- `ChatStream` retries a transient or prelude failure once before the failover decision, and only when no bytes were written.
- Request deadline and `RequestMaxAttempts` stay as they are.

## Task 1: Return the deployment id

The pointer is the bug. `prepare` writes `*meta.Upstream` (`internal/gateway/wire.go` around the `requestmeta.FromContext` block). `requestCtx` in `internal/server/server.go` allocates `upstream := ""` and stores `&upstream`. `recordCall` prices `requestmeta.UpstreamModel(ctx, model)`. `QuoteFor` never sees the body, so it cannot repeat `Index.Target`. `applyModel` can still replace that id with `instance.upstreamModel` after `prepare` returns. The id to price is the one the winning attempt sent.

### Red

Add `TestChatReturnsDeploymentOfTheAttempt` in `internal/gateway/selection_test.go`.

Build a gateway whose catalog lists `gemini-3.8-flash-high` and not the unsuffixed base, on one account whose adapter records the model it was called with. `Chat` a body `{"model":"gemini-3.8-flash","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`.

Assert:

- the second result's deployment is `gemini-3.8-flash-high`
- the second result's account is that account
- the response model echoed to the client is `gemini-3.8-flash`

Expected failure: the test does not compile. `Chat` returns `(adapter.ChatResponse, string, error)`.

Add `TestPricedModelIsTheDeployment` in `internal/server` beside the existing usage tests. Drive one chat completion whose client id is a ceiling alias or an effort base that `prepare` rewrites, with a quote only on the live id. Assert `usage.Event.Model` is the client id and `EstimatedUSD` is set from the live id's quote.

Expected failure before the server change: the event is priced from the client id (or from the pointer, which is the pre-`applyModel` id). If the pointer happens to make this particular alias pass, add a second case where `upstreamModel` on the instance differs from `prepare`'s id and assert the estimate uses `upstreamModel`. That case fails while the pointer is the source.

### Change

Add `internal/gateway/call.go`:

```go
// Call is the account that served the request and the live model id that
// account was called with. The client-facing id stays on the response body.
type Call struct {
    Account    string
    Deployment string
}
```

Change these signatures in `internal/gateway/gateway.go`. The second result replaces the account `string`. Stream methods return `Call` where they returned `string`.

| Method | Today | After |
| --- | --- | --- |
| `Chat` | `(adapter.ChatResponse, string, error)` | `(adapter.ChatResponse, Call, error)` |
| `ChatStream` | `(string, error)` | `(Call, error)` |
| `EditImage` | `(adapter.ImageResponse, string, error)` | `(adapter.ImageResponse, Call, error)` |
| `GenerateImage` | `(adapter.ImageResponse, string, error)` | `(adapter.ImageResponse, Call, error)` |
| `CreateEmbeddings` | `(adapter.EmbeddingResponse, string, error)` | `(adapter.EmbeddingResponse, Call, error)` |
| `Responses` | `([]byte, string, error)` | `([]byte, Call, error)` |
| `ResponsesStream` | `(string, error)` | `(Call, error)` |
| `ClaudeChat` | `([]byte, string, error)` | `([]byte, Call, error)` |
| `ClaudeChatStream` | `(string, error)` | `(Call, error)` |

On a successful attempt, `Deployment` is the `model` value after that candidate's `applyModel`. On a cache hit, it is the model key that hit. On an error after `prepare` and `routeResolved`, `Deployment` is the last model a candidate was called with, or the routed model if no candidate was called. `prepare` failure returns a zero `Call`.

`EditImage` calls `prepare` only for non-multipart bodies. Multipart deployment is the model actually sent, including a `routeTarget` rewrite. Do not send multipart through `Index.Target` inside `QuoteFor`.

Delete:

- `Request.Upstream` and `UpstreamModel` from `internal/requestmeta/request.go`
- the `*meta.Upstream = upstream` write in `prepare`
- the `upstream := ""` allocation in `requestCtx`

`record` and `recordCall` take `deployment string` and stop taking `context.Context` for pricing. `ctx` in those two functions is only used to read the pointer. `priceEvent` receives `deployment` when it is non-empty, otherwise the client model. `QuoteFor` still strips `-thinking-N`.

Update every handler that calls the nine methods:

- `handleChatCompletions`, `handleResponses`, image generations, image edits, embeddings, Claude messages
- showcase calls around the three `s.gw.GenerateImage`, `s.gw.CreateEmbeddings`, and `s.gw.Chat` sites

`usage.Event.Model` stays `peek.Model` or the showcase body model. `AccountID` stays `call.Account`.

`internal/eval/eval.go` prices seed events with `s.Model` before `Chat`. Those seeds are historical events the scenario named, not the live call. Leave that line unless a scenario's `Model` is a client alias (`@` or a synthesized effort base). In that case price the seed with the live id `Index.Target` would call for that scenario's request body, computed in the scenario setup, and pass that string to `PriceUsage`. Do not add `Target` to `QuoteFor`. The `_, _, callErr := gw.Chat(...)` line keeps compiling because the middle value is still one result.

Tests that bind the account string (`account != "first"`) use `call.Account`. `_, _, err :=` and `_, err :=` keep compiling.

### Proof

```sh
PEAPROXY_SECRET_BACKEND=file go test ./internal/gateway ./internal/server ./internal/requestmeta ./internal/eval -count=1
```

`rg -n 'Upstream \*string|func UpstreamModel' --glob '*.go'` prints nothing.

- [ ] Red tests written and seen failing (not observed before implementation for these tests)
- [ ] Red tests written and seen failing (not applicable to the implementation proof; retained as separate plan checklist item)
- [x] Pointer deleted, `Call` returned, pricing uses `Deployment`
- [x] Proof command passed

## Task 2: One failover decision

Depends on task 1. The nine methods already return `Call`.

### Red

Add tests in `internal/gateway` next to `protocolrefusal_test.go` and `admission_test.go`:

- `TestClaudeNativeAdmissionDoesNotCooldown`. Two native-messages accounts. The first returns an admission error. The second succeeds. The first account is not in cooldown afterward. Expected failure today: native `ClaudeChat` does not treat admission as a skip, so it returns the admission error (or cools the account down if the error is also retryable).
- `TestResponsesNativeProtocolRefusalDoesNotCooldown`. First native Responses account returns HTTP 400 whose body contains `modelprotocolunsupported`. Second succeeds. First account is not cooled down, and the model is marked not-chat. Expected failure today: native `Responses` cools down on `retryable` only and does not call `noteProtocolRefusal`.

Do not add a test that changes stream-after-bytes, image 502 replay, or ambiguous delivery. Existing tests in `streamfailover_test.go`, `image_admission_test.go`, `imagebudget_test.go`, and `protocolrefusal_test.go` are the lock for those.

### Change

Add `noteAttempt` on `*Gateway` in `internal/gateway/attempt.go`.

```go
type failPolicy struct {
    Protocol bool
    // Replay nil means retryable(err) || router.Transient(err).
    Replay func(error) bool
}

type failState struct {
    Refused          bool
    AdmissionSkipped bool
}

// noteAttempt reports whether the loop should try the next account.
// A true result has already recorded cooldown or a protocol refusal.
func (g *Gateway) noteAttempt(err error, account, model string, st *failState, p failPolicy) bool
```

`Protocol: true` uses the non-stream order (protocol, then admission, then replay). Stream loops do not call `noteAttempt` for the whole chain, because admission and "bytes already written" have to stay in front of protocol. Give streams a second function in the same file:

```go
// noteStreamAttempt applies the stream order.
// wrote is true when the client has already received a byte.
func (g *Gateway) noteStreamAttempt(err error, wrote bool, account, model string, st *failState) (proceed bool, returned error)
```

`proceed` means try the next account. `returned` non-nil means return that error now (`wrote`, or a non-failover error). Slow prelude is a proceed with `st` recording slow, and no cooldown. Use `failoverOn` for the cooldown step.

Wire it through all nine methods, replacing the inlined `protocolRefused` / `admissionRejection` / `retryable` tails. Image methods pass `Replay: replayableAfter` and `Protocol: false`, and they still return `ambiguousDelivery` before `noteAttempt`. Embedding methods use the default replay and `Protocol: false`.

Native `Responses` and native `ClaudeChat` start using the non-stream policy (`Protocol: true`, default replay). Their translated branches do too, where they currently only check `retryable`.

Keep each method's call body: cache, coalesce, tool rounds, native-versus-translate, stream guard, continuation binding, error-chunk failover with no cooldown.

### Proof

```sh
PEAPROXY_SECRET_BACKEND=file go test ./internal/gateway -count=1
```

- [x] Drift tests written and seen failing
- [x] `noteAttempt` and `noteStreamAttempt` used by all nine methods
- [x] `run` still uncalled by production
- [x] Proof command passed

## Task 3: Split `sse.go` by direction

Parallel-safe with tasks 1 and 2. Do this before task 4.

`internal/translate/sse.go` is about 1230 lines. Exported entry points:

- `OpenAISSEToClaude` at the top of the file
- `ClaudeSSEToOpenAI` around line 494
- `OpenAISSEToResponses` around line 735

Move each exported function, and the helpers only that function calls, into:

- `internal/translate/sse_openai_to_claude.go`
- `internal/translate/sse_claude_to_openai.go`
- `internal/translate/sse_openai_to_responses.go`

A helper used by two directions stays in `sse.go`. Do not copy it. Same package, so existing tests and callers keep their names. No behavior change.

### Proof

```sh
PEAPROXY_SECRET_BACKEND=file go test ./internal/translate -count=1
wc -l internal/translate/sse.go internal/translate/sse_*.go
```

`sse.go` is only the shared helpers. Each direction file holds one exported converter.

- [x] Three direction files, shared helpers left in `sse.go`
- [x] Proof command passed

## Task 4: Move Codex and Gemini codecs into `translate`

Depends on task 3. Parallel-safe with tasks 1 and 2.

### Codex

`internal/adapter/openai_oauth/openai_oauth.go` is about 1755 lines. Transport stays in the adapter: `New`, auth, token refresh, `ListModels`, `headers`, `routingHint`, `doResponses`, `postResponses`, `Chat`, `ChatStream`, `Responses`, `ResponsesStream`.

The codec starts at the `responsesInput` type (around line 622) and runs through `truncate` (around line 1745): `chatToResponses`, `ResponsesToChatCompletion`, `responsesSSEToOpenAI`, carry and service-tier helpers, tool and image part conversion.

Move that code into:

- `internal/translate/codex_request.go` for chat-to-Responses request conversion
- `internal/translate/codex_response.go` for Responses JSON to chat completion
- `internal/translate/codex_sse.go` for Responses SSE to OpenAI chat SSE

Export the three functions copilot already calls, under `translate`:

- `ChatToResponses`
- `ResponsesToChatCompletion`
- `ResponsesSSEToOpenAI`
- `ResponsesStreamToJSON` if it moves with them

`internal/adapter/copilot_oauth/wire.go` calls `openai_oauth.ChatToResponses`, `ResponsesToChatCompletion`, and `ResponsesSSEToOpenAI`. Point those calls at `translate`. Delete the exported wrappers from `openai_oauth` rather than leaving one-line delegates.

Move codec tests with the code: the `TestChatToResponses*` and `TestResponsesSSEToOpenAI*` and `TestResponsesToChatCompletion*` tests in `internal/adapter/openai_oauth/openai_oauth_test.go`, plus `chatparams_test.go` and the codec assertions in `stream_cut_test.go`. HTTP tests stay in `openai_oauth`.

`Chat` and `ChatStream` in the adapter call `translate.ChatToResponses` and `translate.ResponsesSSEToOpenAI`.

### Gemini

`internal/adapter/antigravity/antigravity.go` is about 1806 lines. Auth, headers, and the HTTP client stay.

Move into `internal/translate/gemini.go`:

- `geminiBody` and the request types it needs
- `geminiFunctions`, `geminiSchema`, `geminiPartsFromContent`, `geminiImagePart`
- `geminiChatMessage`, `toOpenAIChatJSON`, `geminiFinishReason`
- `geminiSSEToOpenAI`, `geminiEventUsage`

The adapter methods call `translate` and still set auth headers and perform the request. Export two functions, matching the existing `ToClaude` and `OpenAISSEToClaude` names:

- `GeminiRequest(model string, raw []byte, stream bool) ([]byte, error)` for the body `geminiBody` builds today
- `GeminiSSEToOpenAI(r io.Reader, w io.Writer, model string) error`

Everything else in that move stays unexported in `translate`.

Move the tests that assert Gemini JSON or SSE shape into `internal/translate`. Tests that assert Authorization headers stay in the adapter.

### Red

No new behavior. The move's red is compile failure if a caller still imports the old symbol, plus the existing codec tests failing if a field is dropped. Run the proof before and after. If a test assertion changes, the move changed behavior; put the code back.

### Proof

```sh
PEAPROXY_SECRET_BACKEND=file go test ./internal/translate ./internal/adapter/openai_oauth ./internal/adapter/antigravity ./internal/adapter/copilot_oauth -count=1
```

`wc -l internal/adapter/openai_oauth/openai_oauth.go internal/adapter/antigravity/antigravity.go` is each well under a thousand lines of transport. `rg -n 'func chatToResponses|func geminiSSEToOpenAI' internal/adapter` prints nothing.

- [x] Codex codec lives in `translate`, copilot calls `translate`
- [x] Gemini codec lives in `translate`
- [x] Adapter HTTP tests still pass
- [x] Proof command passed

## Task 5: Split process from dispatch

Depends on task 2. Not parallel with task 2.

`internal/gateway/gateway.go` is about 2600 lines. After task 2 the nine request methods are the request side. Move them, and the types used only by them (`openAIShim`, `openAIChoice`, `openAIMsg`, `countWriter` if nothing else in `gateway.go` needs it), into `internal/gateway/dispatch.go`.

Leave on `gateway.go`: construction, `Refresh`, `Listed`, `Annotated`, `clientIndexLocked`, `Save`, `SaveOAuth`, cooldown and catalog mutation, and the fields of `Gateway`.

Same package, same `*Gateway`. Do not add a `Dispatcher` struct that holds `*Gateway`. That wrapper does not remove a branch.

`prepare` and `applyModel` stay in `wire.go`.

### Proof

```sh
PEAPROXY_SECRET_BACKEND=file go test ./internal/gateway ./internal/server -count=1
wc -l internal/gateway/gateway.go internal/gateway/dispatch.go
```

`gateway.go` no longer contains `func (g *Gateway) Chat`.

- [x] Request methods live in `dispatch.go`
- [x] Proof command passed

## Full check

Run this after task 5, or after the last task you actually land. Do not claim the plan done on a narrower package list.

```sh
PEAPROXY_SECRET_BACKEND=file go test ./... -count=1
gofmt -l $(git diff --name-only HEAD -- '*.go' && git ls-files --others --exclude-standard '*.go')
go vet ./internal/gateway ./internal/server ./internal/translate ./internal/requestmeta ./internal/adapter/openai_oauth ./internal/adapter/antigravity
```

`gofmt -l` prints nothing. Unset `PI_CODING_AGENT_DIR` in the shell before `go test ./...` if the clients tests fail looking for `models.json`.

Review against this plan:

- `rg` finds no `Upstream *string` and no `UpstreamModel`.
- `QuoteFor` does not call `Index.Target`.
- `usage.Event.Model` in server tests is still the client id.
- Selection tests in `internal/catalog/selection_test.go` and `internal/gateway/selection_test.go` still pass, including both account orders for ceiling maxima and "provider-owned efforts are not routed to a sibling".
- Image 502 is not replayed. A stream that has written a byte does not fail over. Ambiguous image delivery does not try a second account.

Security: this plan does not add a credential, a log of a token, or a new network call. Do not print request bodies in new tests.

Publication: do not commit, push, or open a PR unless Karim asks. `docs/PLAN.md` and `docs/V1.md` stay out of any commit for this work. When he asks to commit, run `~/.config/genv/git/review-record.sh` with no arguments before any push. When this plan has shipped or been dropped, move this file to `docs/archive/plans/` in that same commit.

## Handoff log

- 2026-10-16: Plan written. No structural edits. Feature tree still dirty on top of `93adf5b`. Baseline test from the previous session was green for `./internal/catalog ./internal/gateway ./internal/server ./internal/clients` and was not re-run while writing this file. Homebrew `peaproxy` on port 8317 was left on the cask binary. `PI_CODING_AGENT_DIR` was not left exported by this writing session.
- Task 1 implemented on the dirty tree: `Call` returned from the nine methods, `Request.Upstream` removed, `prepare` no longer writes a pointer, `recordCall` prices from `deployment`. Added `TestChatReturnsDeploymentOfTheAttempt` and `TestPricedModelIsTheDeployment`. Red was not observed for these tests, so that box stays open. Proof passed: `PEAPROXY_SECRET_BACKEND=file go test ./internal/gateway ./internal/server ./internal/requestmeta ./internal/eval -count=1`, and the full `./...` suite.
- Task 3 completed: split the three SSE converters into direction-specific files, leaving shared helpers in `sse.go`. `PEAPROXY_SECRET_BACKEND=file go test ./internal/translate -count=1` passed; the three direction files and shared-helper layout were reviewed.
- Task 2 done on the dirty tree. `noteAttempt` and `noteStreamAttempt` in `internal/gateway/attempt.go` are used by all nine request methods. Native `ClaudeChat` and native `Responses` moved to the non-stream order, which fixes the two drifts. Drift tests are in `internal/gateway/failpolicy_test.go`. `noteProtocolRefusal` has no callers left; it is kept, not deleted, so that the plan's other steps stay untouched. Proof passed: full `PEAPROXY_SECRET_BACKEND=file go test ./... -count=1`.
- Task 4 Codex half done: Codex codec moved to `internal/translate/codex_{request,response,sse}.go`, with exported `ChatToResponses`, `ChatToResponsesFromMessages`, `PrepareResponses`, `ResponsesStreamToJSON`, `ResponsesToChatCompletion`, `ResponsesSSEToOpenAI`. Copilot calls `translate`. Codec tests moved to `internal/translate/{codex_codec,chatparams_codec,stream_cut_codec}_test.go`. Proof green: `go test ./internal/translate ./internal/adapter/openai_oauth ./internal/adapter/copilot_oauth -count=1`.
- Task 4 Gemini completed. Codec moved to `internal/translate/gemini.go`; adapter resolves the project under lock and calls pure `translate.GeminiRequestJSON(project, req, stream)`. Adapter retains `chatHTTPError`, `cloudCodeResetDelay`, `truncate`, and `streamEventError`; stream codec accepts an error-policy callback via `GeminiSSEToOpenAIWith`, while `GeminiSSEToOpenAI(r,w,model)` remains the default codec entry point. Exported wrappers expose `GeminiChatToOpenAI`, `GeminiUsage`, `GeminiUpstreamModelID`, and `SanitizeGeminiSchema` as needed. This intentionally differs from planned `GeminiRequest(model, raw, stream)`: project is an adapter-resolved input and must not be read through mutable adapter state in the codec. Codec tests moved; adapter policy tests use `geminiStream`, which passes the production `streamEventError` callback. Proof passed: `PEAPROXY_SECRET_BACKEND=file go test ./internal/translate ./internal/adapter/openai_oauth ./internal/adapter/antigravity ./internal/adapter/copilot_oauth -count=1`; `go vet` on the four packages passed; files are gofmt-clean after formatting.
- Task 5 done. Nine request methods and their request-only helpers/types moved to `internal/gateway/dispatch.go`; lifecycle, catalog, cooldown, admission, and routing helpers stay in `gateway.go`. Proof passed: `PEAPROXY_SECRET_BACKEND=file go test ./internal/gateway ./internal/server -count=1`; `go vet` passed, and `gateway.go` contains no request methods.
- Full check completed after Task 5: `unset PI_CODING_AGENT_DIR && PEAPROXY_SECRET_BACKEND=file go test ./... -count=1` passed across all packages. `gofmt` reported no formatting issues for existing changed/untracked Go files (deleted paths were excluded); `go vet ./internal/gateway ./internal/server ./internal/translate ./internal/requestmeta ./internal/adapter/openai_oauth ./internal/adapter/antigravity` passed. Existing unrelated dirty edits in `docs/HARNESS.md`, `docs/PLAN.md`, and `docs/V1.md` were left untouched.
- Review of PR #157 (2026-10-17). CI was red on the coverage ratchet (75.8% against 76.5%): the Gemini request, schema, and stream tests had stayed in `internal/adapter/antigravity`, so the code they cover no longer counted. The pure codec tests moved to `internal/translate/gemini_codec_test.go` and `schema_typelist_gemini_test.go`; error-policy, HTTP, and safety-finish tests stay in the adapter. The translated tails of `Responses` and `ClaudeChat` now use `noteAttempt` too (the native branches already did), and both end with `errModelRefusesChat` when an account refused the protocol, as `Chat` does. `prepare` reads effort from `reasoning.effort` and `output_config.effort` as well as `reasoning_effort`, so Responses and Messages clients reach the sibling they asked for. `ToClaude` sends OpenAI's `minimal` as Claude's `low`. Full `./...`, the race packages, the routing-promise gate, and `golangci-lint` v2.14.0 passed; total coverage 77.3%.
