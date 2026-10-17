# Changelog

## 3.2.0

- Published context and effort facts. `GET /v1/models` carries `context_window`, `max_context_window`, `max_tokens`, `reasoning`, `efforts`, and `hide_effort_off` when the provider published them (Anthropic, Codex, Copilot). Nothing is inferred from a model name.
- Context-ceiling ids. A model whose provider published a larger ceiling than its default window is also listed as `{model}@{ceiling}`, which calls the same live model. Pi import carries these, with `thinkingLevelMap` from the published efforts.
- Effort siblings. Live ids that differ only by an effort suffix (`gemini-3.8-flash-low`, `-medium`, `-high`) gain a base id when the base is not already live. The request's effort picks the sibling: `reasoning_effort` on chat, `reasoning.effort` on Responses, `output_config.effort` on Messages, and `medium` when none is named.
- A chat `reasoning_effort` reaches Claude as `output_config.effort` with adaptive thinking and the `effort-2025-11-24` beta. OpenAI's `minimal` is sent as `low`.
- Usage is priced from the model the winning attempt actually called. The client-facing id stays on the response and on the usage event.
- One failover policy across the nine request paths. Native and translated Responses and Messages now skip a busy account without a cooldown and treat a protocol refusal as a property of the model, as Chat already did.
- Internal: request methods move to `internal/gateway/dispatch.go`, and the Codex and Gemini codecs move into `internal/translate`.

## 3.1.2

Documented release on main. Not tagged.

- Poe browser login (`poe_oauth`) using the public OpenCode client, PKCE on an ephemeral `127.0.0.1` port, and OpenAI-compatible chat at `https://api.poe.com/v1`. No refresh token. Untested.
- Z.AI Coding Plan login (`zai_oauth`): ZCode CLI poll at `zcode.z.ai`, then a Coding Plan key, then OpenAI-compatible chat at `https://api.z.ai/api/coding/paas/v4`. Distinct from the pay-as-you-go `zai` API-key preset. Untested.
- Kilo and MiniMax logins are labeled untested in the docs, CLI help, and account presets (`unverified` on `GET /admin/presets`).
- Untested logins: please report what happened at https://github.com/ks1686/peaproxy/issues

v3.0.9 and v3.1.0 are already published tags. v3.1.1 was the previous documented release and was not tagged. This change does not move the documented version back to v3.0.9.

GitLab Duo is not included. Its public gitlab.com client and OAuth endpoints are documented, but chat is split across a direct-access token, two AI-gateway proxies, the OpenAI Responses API, and a Workflow WebSocket. Self-managed GitLab would need an OAuth app this project will not register.
