# Live provider verification — the locally configured accounts

Date: **2026-10-11** (UTC; 2026-10-10 local).
PeaProxy under test: **v3.0.9**, commit `d57a0eb` (`git describe`: `v3.0.9-1-gd57a0eb`),
running as the user's own instance on `127.0.0.1:8317` with the real config at
`~/Library/Application Support/peaproxy/config.yaml`. This is the shipped binary,
not a branch build.

Client: `curl` against the public surfaces (`/v1/chat/completions`,
`/v1/messages`, `/v1/responses`), plus `GET /admin/health`, `GET /admin/catalog`,
`GET /admin/usage` for the ledger.

Why this page exists: the readiness plan marks the live checks (R5) as the only
part of the audit that cannot be done offline, and every other provider claim in
this repository is documentation-derived. This is the record for the eight
accounts that were actually configured on this machine. It supersedes nothing;
the Cohere record in [2026-10-05-v3-provider-evidence.md](2026-10-05-v3-provider-evidence.md)
stands as the only key-based provider verified before this run.

Cost control: one minimal non-stream and one minimal stream request per account,
prompt `hi`, `max_tokens` 16 (raised to 32 where a model returned nothing).
16 requests plus 4 native-wire requests and 14 follow-ups. Every model chosen
was the smallest or cheapest that account offered.

> **Status of the usage findings: fixed after this run.** The token counts in the
> table below are what **v3.0.9** recorded, and they are the evidence for two
> defects that have since been fixed on this branch. The fixes are recorded in
> [Fixed since this run](#fixed-since-this-run); the table is left as measured.
> Re-run against a build of this branch to confirm the corrected behaviour on
> real accounts.

## Result summary

| Account | Adapter | Model used | Chat wire | Native wire | Tokens recorded |
|---|---|---|---|---|---|
| `anthropic-oauth` | `anthropic_oauth` | `claude-haiku-4-5-20251001` | 200 / 200 | Messages 200 / 200 | native both paths; **chat stream none** |
| `openai-oauth` | `openai_oauth` | `gpt-5.6-luna`, `gpt-reserve` | 200 / 200 | Responses 200 / 200 | native both paths; **chat neither** |
| `google-oauth` | `antigravity` | `gemini-2.5-flash-lite` | 200 / 200 | — | **neither path** |
| `copilot-oauth` | `copilot_oauth` | `gpt-5.4-mini` | **503 (429 upstream)** | — | n/a, refused |
| `xai-oauth` | `xai_oauth` | `grok-4.7-build-fast` | 200 / 200 | — | chat direct only; **chat stream none** |
| `google-key` | `google` | `models/gemini-3.5-flash-lite` | 200 / 200 | — | chat direct only; **chat stream none** |
| `opencode-zen` | `opencode_zen` | 4 free models | 200 / 200 | — | chat stream only; **chat direct none** |
| `workers-ai` | `workers_ai` | `@cf/meta/llama-3.2-1b-instruct` | 200 / 200 | — | **both paths (12/10)** |

Seven of eight accounts answered. No account needed a new login — see
[Auth state](#auth-state-no-account-needed-re-login).

## Auth state: no account needed re-login

Every OAuth account still held a working **refresh token**. Two of them were
refreshed *during this run*, which is the direct evidence:

| Account | `expiresAt` before | `expiresAt` after | Meaning |
|---|---|---|---|
| `google-oauth` | `2026-10-10T20:13:25Z` (expired) | `2026-10-11T02:57:22Z` | refreshed and persisted without a browser |
| `xai-oauth` | `2026-10-11T01:13:30Z` (expired) | `2026-10-11T07:58:08Z` | refreshed and persisted without a browser |
| `anthropic-oauth` | `2026-10-11T03:15:51Z` | unchanged | still valid, no refresh needed |
| `openai-oauth` | `2026-10-16T15:07:15Z` | unchanged | long-lived |
| `copilot-oauth` | `2026-10-11T19:13:29Z` | unchanged | still valid; blocked on quota, not auth |

So the "expiry" that shows up in `config.yaml` is the **short-lived access
token**, not the credential. It lapses roughly hourly and only advances when a
request triggers a refresh, so an idle account always *looks* expired. Nothing
was wrong with any account, and no `auth login` browser flow was run.

## Findings

### Fixed since this run

Findings 1 and 2 below were both real defects in PeaProxy and both are now fixed
on this branch. What was changed:

| Fix | Where | Test that pins it |
|---|---|---|
| Translate Anthropic `message_start` / `message_delta` usage into an OpenAI `usage` object | `internal/translate/sse.go`, `ClaudeSSEToOpenAI` | `TestClaudeSSEToOpenAIReportsUsage`, `TestClaudeSSEToOpenAIWithNoUsageEmitsNoUsageFrame` |
| Capture `response.completed.usage` on the Responses wire | `internal/adapter/openai_oauth/openai_oauth.go`, `responsesSSEToOpenAI` | `TestResponsesSSEToOpenAIReportsUsage`, `TestResponsesSSEToOpenAIWithNoUsageReportsNone` |
| Capture Gemini `usageMetadata`, streamed and non-streamed | `internal/adapter/antigravity/antigravity.go`, `geminiSSEToOpenAI`, `openAIUsageFromGemini` | `TestGeminiStreamReportsUsage`, `TestNonStreamChatCarriesUsage`, `TestNonStreamChatWithNoUsageReportsNone`, `TestNonStreamChatWithOneSidedUsageReportsNone` |
| Carry `usage` from an assembled Responses body into the chat.completion | `internal/adapter/openai_oauth/openai_oauth.go`, `responsesToChatCompletion` | `TestResponsesToChatCompletionCarriesUsage`, `TestResponsesToChatCompletionWithNoUsageOmitsIt`, `TestResponsesToChatCompletionWithOneSidedUsageReportsNone` |
| Ask for in-stream usage (`stream_options.include_usage`) on the OpenAI-compatible wire | `internal/adapter/openai_compat/openai_compat.go`, `ChatStream` | `TestChatStreamAsksForStreamUsage`, `TestChatStreamLeavesAnExplicitStreamOptionsAlone`, `TestChatStreamKeepsTheRestOfTheBodyIntact`, `TestNonStreamRequestDoesNotCarryStreamOptions` |
| End-to-end: a streamed call records the tokens the provider published, through the public surface | `internal/server/usage_stream_test.go` | `TestStreamedCallRecordsUsageFromTheUpstream`, `TestStreamedCallWithNoUsageUpstreamStaysUnmeasured` |

Two rules the fix holds to, both covered by tests: a usage frame is written only
when **both** halves of the count are present, so a call that was never measured
is not recorded as one that consumed nothing; and a client that set
`stream_options` itself keeps it, because that is a decision about the response
shape made by whoever owns the body.

Deliberately left open rather than half-fixed: Anthropic cache tokens
(`cache_read_input_tokens`, `cache_creation_input_tokens`) are reported beside
`input_tokens` rather than inside it, and neither the streamed nor the
non-streamed path counts them, so a cached Claude call under-reports its prompt.
`claudeNative.Usage` parses only input and output. This is recorded as a known
limitation in [V3.md](../V3.md).

Not re-verified against live accounts yet — the fixes are covered by tests that
drive the real translators and the real server path, but the live re-run has not
been done.

### 1. Streamed calls lose their usage on the translated chat wire *(fixed)*

The largest finding, and it is systematic. `internal/server.sseWriter` records
stream usage by capturing the last chunk containing `"usage"`
(`internal/server/server.go:1325`). On a **native** wire the provider's own usage
frame passes through and is captured. On the **translated** OpenAI-chat wire
nothing ever emits one: `grep '"usage"' internal/translate/*.go` finds only the
*inbound* Anthropic structs, and no code writes an OpenAI `usage` object into an
outgoing stream chunk.

Measured, same account, same model, same prompt, one variable changed:

| Account | `/v1/chat/completions` direct | `/v1/chat/completions` stream | native wire |
|---|---|---|---|
| `anthropic-oauth` | `22/16` | **none** | Messages: `22/32` direct **and** `22/32` stream |
| `xai-oauth` | `1243/9` (cacheRead 1152) | **none** | — |
| `openai-oauth` | **none** | **none** | Responses: `7/13` direct **and** `7/13` stream |
| `google-key` | `2/10` | **none** | — |
| `workers-ai` | `12/10` | `12/10` | — |
| `opencode-zen` | **none** | `428/16` | — |

Consequence: for a streaming subscription session the ledger records a call with
no tokens at all, so a spend ceiling over that account cannot be trusted and the
usage panel shows nothing. The native wires prove the data is available — the
loss is introduced by the translation layer, not by the providers.

`google-key` is a second, independent instance of the same class: Google is a
thin `openai_compat` pass-through, and its OpenAI-compatible endpoint only emits
an in-stream usage frame when the request asks for it. `openai_compat.ChatStream`
does not inject `stream_options: {"include_usage": true}`, so usage is available
but never requested. The same injection would fix every OpenAI-compatible
provider that gates usage on it.

### 2. Non-stream translated calls lose usage for two accounts *(fixed)*

`openai_oauth` and `antigravity` recorded no tokens on either chat-wire path,
while both recorded tokens correctly on their native wire (`/v1/responses`
`7/13`). Both providers put the counts in the body PeaProxy was already reading;
the translation read the text and left the usage behind. Fixed.

**`opencodezen` is not a defect and was not changed.** Checked directly against
the live instance on 2026-10-11: a free model asked non-stream returns a body
with keys `choices` and `object` only — no `usage`, and no `id`, `model` or
`created` either. There is nothing to translate, so the call is recorded as
unmeasured, which is the correct outcome. The streamed shape of the same model
does publish usage, which is why the two paths disagreed.

### 3. No call in this run produced a cost figure

Every event had `costUSD: null` and `estimatedUSD: null`, including the four
paid-subscription accounts. This config has no `automaticRoutes.prices`, so
PeaProxy has no quote to estimate from, and subscription providers publish no
per-call cost. This is the documented "unknown is not zero" behaviour working
as intended — it is recorded here so the absence is not later mistaken for a
free call.

### 4. `copilot-oauth` is quota-blocked, and the displayed quota is misleading

```
upstream HTTP 429: quota exceeded
```

PeaProxy cooled the account for ~57 minutes. But the header-derived quota on the
same account reports `remainingRequests: 14749 / limitRequests: 15000` — 251
used of 15000 — captured at `2026-10-10T19:13:29Z`, roughly seven hours before
the refusal. The number on screen is therefore **not** the budget that governed
the refusal; Copilot enforces a shorter window that PeaProxy does not observe.
An account can display "14749 remaining" while every request is refused, which
is the kind of quiet contradiction this project exists to avoid.

### 5. Catalog offers retired Google models

`google-key` lists 62 models from live `ListModels`, including
`models/gemini-2.5-flash-lite`. Calling it returns HTTP 404:

> This model models/gemini-2.5-flash-lite is no longer available to new users.
> Please update your code to use models/gemini-3.5-flash-lite

The id format is fine — Google echoes it back. The catalog simply cannot tell a
retired model from a live one, because `ListModels` publishes no deprecation
field. `models/gemini-3.5-flash-lite` and `models/gemini-3.1-flash-lite` both
answered 200 on both paths. Note the model itself may be dropped from
`/v1/models` after a first refusal (see the readiness plan's
`ModelProtocolUnsupported` rule), which is why the failure is self-limiting.

### 6. One Zen free model is not served on the chat protocol

`jev-1.13-free` returned 502 `unsupported_api_for_model` — "this provider does
not serve that model on the chat protocol". It was then removed from
`/v1/models`, and the follow-up stream attempt got 404 "no account available
for model", which is the documented drop-after-refusal behaviour working. Four
other free models (`space-bunny-free`, `mimo-v2.6-flash-free`,
`nemotron-3.5-lightning-free`, `step-5-preview-free`) answered 200 on both paths.

### 7. Two operational observations, not defects

- `models/gemini-3.8-flash` took **40.3 s** for a one-word prompt on the
  non-stream path while the same model streamed in 2.4 s. The same request on
  `gemini-3.5-flash-lite` took 0.59 s.
- Several Zen free models returned `finish_reason: stop` with **empty content**
  at `max_tokens: 16`. Raising the budget to 32 returned `"Hello!"`. The
  truncation reported `stop` rather than `length`, which is misleading, but no
  content was lost.

## What this changes

- The subscription adapters are **live-verified** for: model listing, chat
  non-stream, chat stream with a correct terminal event, and native-wire
  pass-through with usage. `copilot-oauth` is verified for listing only; its
  chat path is quota-blocked at the time of writing.
- Findings 1 and 2 are usage-reporting defects in PeaProxy, not provider
  behaviour, and they are the reason a streaming subscription session shows
  nothing in the usage panel.
- No preset here moves to `live-verified` for *interoperability breadth*: this
  run proved listing, one tiny chat shape, and terminal events. Tool calling,
  vision, images, embeddings and error classification remain unexercised, and
  the readiness plan's R5 items stay open for them.