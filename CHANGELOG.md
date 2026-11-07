# Changelog

## 3.3.7

- Routes: a pinned route's cooldown now names the pinned account. `cooldownCause` walked every account that lists the target, so `xai-grok-4.7` with the xAI account cooling reported `copilot-oauth` — the one account the request was never going to use. It is filtered the same way the candidate list is.

## 3.3.6

- Routes: a route name can pin the account it calls, so one model id served by several providers can be offered as distinct client-facing names. `routes: {copilot-gpt-6-sol: {target: gpt-6-sol, account: copilot-oauth}}` calls only `copilot-oauth` and does not fail over; a cooled pinned account surfaces as a cooldown naming it. The catalog row names the account. The short form (`routes: {code: llama3.2}`) still means every account that lists the target, and saving a config never reshapes an unpinned route. `account` must be an id in `providers`.

## 3.3.5

- Claude OAuth: the Praison AI skill trio (`skill_manage`, `skill_view`, `skills_list`) is aliased like OpenCode's core tools, so a request carrying Hermes's 20-tool set no longer trips Anthropic's "Third-party apps now draw from extra usage" refusal even when the workspace has usage. Claude Code exposes a single `Skill` tool, so the first of the trio is sent as `Skill` and the other two keep their own names; a claimed-upstream guard stops two client tools from taking the same upstream name, which the reverse map cannot restore.
- OAuth: a device-code login waits out the code's own lifetime instead of a hardcoded 5 minutes. xAI issues codes that live 30 minutes, so approving after five minutes cancelled the poll, saved no token, and left the account on its old credential -- which then read as "the account is out of usage". `adapter.DeviceWait` clamps the wait to [5m, 15m], both the CLI and the UI use it, and the CLI prints the window it will actually wait.
- Admin: `GET /admin/health` and `POST /admin/catalog/refresh` count client-visible models (`len(gw.Listed(catalog.FilterAll))`) instead of `len(gw.Models())`, so `peaproxy health` and the UI agree after a `hide`/`expose` edit (#172).
- Server/CLI: a clean startup writes the `peaproxy listening on …` line to stdout, so a launchd error log stays empty on a normal boot (#171). `peaproxy status` probes `/healthz` and prints a real `phase:` beside the configured `listen:`; the misleading `status:` (CLI version) became `version:` (#170).

## 3.3.4

- Secret store: a leftover `secrets.<config>.enc` that decrypts to 0 items (file size still > 0) no longer pins the encrypted-file backend. 3.3.3 treated any non-empty enc as file-primary, so a LaunchAgent auto open never read Keychain and a new OAuth login that landed there stayed `oauth-login-required`. An empty `[]` index plus that vacuous blob now falls through to the OS keychain when it works. A file-backend migrate that later prunes the last scoped secret does not then resurrect that token from the never-pruned leftover `secrets.enc` when auto switches to Keychain.

## 3.3.3

- Secret store: a keychain-primary open copies leftover `secrets.enc` / `secrets.<config>.enc` items into the scoped keychain, so accounts that lived only in the encrypted file (Antigravity, OpenAI, …) are not stranded after a partial Keychain migrate. If a Keychain write fails (common for a LaunchAgent with no UI prompt), that secret is saved in the encrypted file instead so OAuth login can still become `oauth-ready`.

## 3.3.2

- Secret store: an empty `secrets.<config>.index` (`[]`) no longer counts as a finished migration. 3.3.1 could write that file on a config save before copying a legacy Keychain token, then skip migration forever, so Claude (`anthropic-oauth`) stayed `oauth-login-required` while the old `peaproxy` / `account/oauth` item was still in Keychain.

## Unreleased

## 3.3.1

- UI OAuth: Accounts login is `complete` only when the account is actually `oauth-ready` (secret stored, `HasOAuth()` true). A token exchange or secret-save failure is `error` with the real message. Previously `AuthComplete` returning nil marked the job complete even when Claude (`anthropic-oauth`) or Antigravity stayed `oauth-login-required` and no `secrets.*.enc` was written (#166, #167, #168).
- Gateway: `PersistOAuth` returns the `saveOAuthFrom` error so UI login fails when the secret was not stored. A refresh that cannot save still keeps the live in-memory token, so catalog discovery and chat stay routable; the save failure is logged and the next successful save writes it.
- Claude OAuth: a 2xx token body without an access token is rejected before persist.
- Antigravity OAuth: login requests the `aicode` scope, discovers the Cloud Code project via `loadCodeAssist` on the daily host (same as `onboardUser` / `generateContent`), and refuses an empty access token, email, or project id instead of storing an unauthenticated account.

## 3.3.0

- Claude OAuth: a request from a native Claude Code build (CLI, Agent SDK CLI or TypeScript SDK, VS Code) reaches Anthropic with that build's own User-Agent, Stainless headers (except the retry count), session id and betas, instead of PeaProxy's fixed 2.1.280 profile. Only `claude-code-20250219` and `oauth-2025-04-20` are added, and `context-1m-*` is still dropped. A request counts as native only when its headers (`User-Agent: claude-cli/x.y.z (external, <entrypoint>)`, `x-app: cli`, and every Stainless and session header) and its body (Claude Code's own billing block and `metadata.user_id`) all say so; every other client keeps the fixed profile. Live-checked with Claude Code 2.1.304 `claude -p`. PeaProxy still signs in with its own Claude login; the client's credentials are never forwarded.
- A client's `anthropic-beta` list is kept up to 32 names (was 16), so a native Claude Code list cannot lose a beta it needs.

## 3.2.1

- Claude OAuth: Anthropic's "Third-party apps now draw from extra usage, not plan limits" refusal reaches the client as error type (and OpenAI `code`) `extra_usage_required`, quoting Anthropic's sentence and `request_id` and naming the fix (an Anthropic API-key account, or extra usage from the workspace admin), instead of a bare `upstream HTTP 400`. It never retries, fails over or cools down: every retry gets the same answer, and the next account may be a metered key. Every other upstream body still stays out of client errors.
- `peaproxy health` (24h) and `peaproxy errors summary` (`--since`) count those refusals per account, including records written by older builds. `health` says the count is unknown when `requestLog` is off.
- Routing: a transport failure (TLS, reset, edge 502/503/504) no longer cools an account when the request had no other candidate, unless the upstream sent `Retry-After`. An account coming out of cooldown goes back into rotation as soon as its probe stream commits, not when that stream ends.

## 3.2.0

- Published context and effort facts. `GET /v1/models` carries `context_window`, `max_context_window`, `max_tokens`, `reasoning`, `efforts`, and `hide_effort_off` when the provider published them (Anthropic, Codex, Copilot). Nothing is inferred from a model name.
- Context-ceiling ids. A model whose provider published a larger ceiling than its default window is also listed as `{model}@{ceiling}`, which calls the same live model. Pi import carries these, with `thinkingLevelMap` from the published efforts.
- Effort siblings. Live ids that differ only by an effort suffix (`gemini-3.8-flash-low`, `-medium`, `-high`) gain a base id when the base is not already live. The request's effort picks the sibling: `reasoning_effort` on chat, `reasoning.effort` on Responses, `output_config.effort` on Messages, and `medium` when none is named.
- A chat `reasoning_effort` reaches Claude as `output_config.effort` with adaptive thinking and the `effort-2025-11-24` beta. OpenAI's `minimal` is sent as `low`.
- Usage is priced from the model the winning attempt actually called. The client-facing id stays on the response and on the usage event.
- One failover policy across the nine request paths. Native and translated Responses and Messages now skip a busy account without a cooldown and treat a protocol refusal as a property of the model, as Chat already did.
- Two subscription key presets (#145): Xiaomi MiMo Token Plan (`mimo`, `MIMO_API_KEY`, Singapore cluster by default; China and Europe keys use their own cluster's base URL) and Charm Hyper (`hyper`, `HYPER_API_KEY`). Both are marked not verified until a real account calls them.
- PROVIDERS.md says Qwen consumer OAuth was discontinued upstream; `qwen_oauth` stays registered only so a saved row fails with that reason.
- Internal: request methods move to `internal/gateway/dispatch.go`, and the Codex and Gemini codecs move into `internal/translate`.

Fixes from a full review:

- Spend: a request releases exactly the spend holds it took, so a refused, cached or local request no longer frees another request's reservation; a response-cache hit is priced at zero instead of making the spend window unmeasurable; N identical coalesced requests are billed once, even when the request that started the call disconnects; a discarded retrieval round releases its hold; a config reload applies `optimization` (the spend ceiling) and `requestEngine.maxInFlight` as documented.
- Usage: streamed usage is kept from every SSE frame, not only the last write; Responses usage says which input tokens were cached; Anthropic cache reads and writes survive translation to chat; Gemini usage is read inside the Cloud Code envelope and counts thinking tokens; non-stream Responses served by a chat upstream carry usage.
- Claude OAuth: the cloak keeps requests within Anthropic's limit of 4 cache breakpoints. Codex: `prompt_cache_breakpoint` is dropped before chatgpt.com sees it; a cut-off buffered stream is an error rather than a truncated answer.
- Streams: Responses streams report `response.failed` / `incomplete` / `error` instead of finishing with `stop`, and no longer leak reasoning text into the answer; Responses lifecycle events are valid JSON and announce a function call before its argument deltas; chat tool calls are numbered from zero; reassembled Zen streams keep tool calls, finish reason and usage; Copilot streams are no longer cut off at 30 seconds; cursor_agent keeps records split across stdout reads.
- Routing: only one request at a time probes an account coming out of cooldown, and `optimization explain` no longer takes that probe or clears cooldowns; streamed Responses follow-ups return to the account that holds `previous_response_id`; the response cache separates thinking budgets; a coalesced call runs with the leader's request context; requests queued before an admission gate's first release are woken; request paths read the config under the lock.
- Requests: a JSON body with adjacent leading keys no longer panics `jsonx.DropTopLevelKeys`; a duplicated key cannot outlive `SetTopLevelRaw`; images nested in Responses items or tool results trigger the vision guard; Messages-to-chat keeps `tool_choice`, `temperature`, `top_p` and `stop_sequences`; a client's own `pea_search` tool is left to the client.
- Server and config: `/admin/hide` bounds its body; a request body that stalls after the headers is cut off; a LAN bind can take its admin token from `PEAPROXY_ADMIN_TOKEN` or `--admin-token`; secrets stay on the backend that holds them instead of splitting after one failed keychain probe; a token refreshed while the accounts were being rebuilt reaches the rebuilt account instead of leaving it with a spent refresh token; saves work when the LAN admin token comes from the environment; a retried OAuth login frees the previous attempt's callback port; adapters share one connection pool.
- Secrets: each config file has its own secrets (`secrets.<config>.enc` / `.index`, keychain items under a per-directory id), so a second config in the same directory no longer prunes the first one's secrets and two directories with an account of the same name no longer share its keychain item. The old shared store is copied into each config's scope the first time 3.2.0 opens it; for 7 days, tokens an old server still running from before the upgrade refreshes into it are carried over (OAUTH.md has the rules and says when the old store can be deleted).
- Streams: an upstream stream that ends without a single event (only pings or `[DONE]`) fails over instead of handing the client an empty 200.
- Clients: connecting Codex selects the PeaProxy provider and model (and disconnect restores the previous ones); connecting Continue merges into an existing `models` key instead of adding a second; the UI's Copy snippet button works.

## 3.1.2

Documented release on main. Not tagged.

- Poe browser login (`poe_oauth`) using the public OpenCode client, PKCE on an ephemeral `127.0.0.1` port, and OpenAI-compatible chat at `https://api.poe.com/v1`. No refresh token. Untested.
- Z.AI Coding Plan login (`zai_oauth`): ZCode CLI poll at `zcode.z.ai`, then a Coding Plan key, then OpenAI-compatible chat at `https://api.z.ai/api/coding/paas/v4`. Distinct from the pay-as-you-go `zai` API-key preset. Untested.
- Kilo and MiniMax logins are labeled untested in the docs, CLI help, and account presets (`unverified` on `GET /admin/presets`).
- Untested logins: please report what happened at https://github.com/ks1686/peaproxy/issues

v3.0.9 and v3.1.0 are already published tags. v3.1.1 was the previous documented release and was not tagged. This change does not move the documented version back to v3.0.9.

GitLab Duo is not included. Its public gitlab.com client and OAuth endpoints are documented, but chat is split across a direct-access token, two AI-gateway proxies, the OpenAI Responses API, and a Workflow WebSocket. Self-managed GitLab would need an OAuth app this project will not register.
