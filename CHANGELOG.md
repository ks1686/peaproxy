# Changelog

## 3.1.2

Documented release on main. Not tagged.

- Poe browser login (`poe_oauth`) using the public OpenCode client, PKCE on an ephemeral `127.0.0.1` port, and OpenAI-compatible chat at `https://api.poe.com/v1`. No refresh token. Untested.
- Z.AI Coding Plan login (`zai_oauth`): ZCode CLI poll at `zcode.z.ai`, then a Coding Plan key, then OpenAI-compatible chat at `https://api.z.ai/api/coding/paas/v4`. Distinct from the pay-as-you-go `zai` API-key preset. Untested.
- Kilo and MiniMax logins are labeled untested in the docs, CLI help, and account presets (`unverified` on `GET /admin/presets`).
- Untested logins: please report what happened at https://github.com/ks1686/peaproxy/issues

v3.0.9 and v3.1.0 are already published tags. v3.1.1 was the previous documented release and was not tagged. This change does not move the documented version back to v3.0.9.

GitLab Duo is not included. Its public gitlab.com client and OAuth endpoints are documented, but chat is split across a direct-access token, two AI-gateway proxies, the OpenAI Responses API, and a Workflow WebSocket. Self-managed GitLab would need an OAuth app this project will not register.
