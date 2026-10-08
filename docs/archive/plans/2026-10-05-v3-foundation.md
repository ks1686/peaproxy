# v3 foundation and provider tasks

Parent: [v3 plan](2026-10-05-v3.md). All commands run from repository root with PEAPROXY_SECRET_BACKEND=file. New tests must first fail for the specified behavior; missing packages alone are not useful red evidence.

## T1 — stream reliability and routing cooldown correctness

Order: serial first; release blocker D1, plus D4.
Files: internal/gateway/stream_terminal_test.go (new), internal/gateway/stream_failure_test.go, internal/gateway/streamfailover_test.go, internal/gateway/engine_route.go, internal/gateway/cooldown.go, internal/gateway/gateway.go, internal/gateway/wire.go, internal/translate/sse.go, internal/translate/sse_tooluse.go, internal/streamguard/guard.go, internal/server/server.go; affected adapter file after trace identifies it.

Input: exact incident and D4 fixture in scope register. Output: correct terminal/error semantics and per-candidate cooldown filtering.

Red: reproduce D1 with sanitized upstream frames on its actual wire; cover EOF/reset before and after commit, no trailing newline, split frames, slow reasoning, fragmented tool arguments, cancellation, request deadlines, terminal flush and simultaneous model cooldowns. A postcommit failure must not trigger a second provider or fabricated success. Include retry exhaustion with a clear final error and no endless retry loop.
Green: fix the traced boundary, not a generic synthetic finish_reason. Evaluate all active cooldown slots for each candidate. Refactor only common lifecycle logic demonstrated by tests.
Proof: `go test ./internal/translate ./internal/streamguard ./internal/gateway ./internal/server`; replay affected client against isolated proxy and authorized upstream. Record timestamp, versions and sanitized IDs. If live reproduction remains unavailable, D1 remains open and v3 cannot be labeled fixed.

## T2 — accurate usage and attempt ledger

Order: after T1; serial with any server/gateway edits.
Files: internal/usage/parse.go, internal/usage/parse_test.go (new), internal/usage/usage.go, internal/usage/day_test.go, internal/server/server.go, internal/gateway/attempt.go, internal/gateway/attempt_usage_test.go (new), internal/promptcache/policy.go.
Input: provider frames and attempt outcomes. Output: per-attempt published token/cache counters and optional cost, aggregated once into request/session totals.

Red: D2/D3 fixtures; explicit zero vs absent counters; Anthropic start/delta merge; nested Responses terminal usage; OpenAI cached tokens; cache writes; repeated terminal frames; partial failed attempts; retries and internal retrieval turns; cached-response hit must not replay original bill. Sum separate attempts, not cumulative samples from the same attempt. Unknown failed-attempt usage stays unknown.
Green: provider/wire-aware accumulators and attempt IDs; separate cache read/write, uncached input, output/reasoning where published. Keep existing usage.json readable. Separate observed costs from estimates with price-source and timestamp.
Proof: `go test ./internal/usage ./internal/promptcache ./internal/gateway ./internal/server`.

## T3 — deployment economics and v3 configuration contracts

Order: after T2; serial contract gate for remaining tasks.
Files: internal/economics/types.go, cost.go, cost_test.go (all new); internal/catalog/pricing.go, pricing_test.go, catalog.go; internal/quota/store.go, parse.go; internal/config/optimization.go, optimization_test.go (new), config.go, engine.go, clone.go, merge.go; internal/compatdata/schema.go; configs/peaproxy.example.yaml.

Output contracts:
- Deployment identity: account ID + endpoint + adapter + concrete model ID; no model-name-only price lookup.
- Quote: currency, input/output/cache read/cache write units and rates, source, observation time, verification status. Missing components remain unknown.
- Allowance: kind (recurring/trial/subscription/none/unknown), remaining amount/unit, reset/expiry, source, overage behavior and account scope. Account-wide shared quotas must not multiply per model.
- Route decision: requested alias/model, concrete deployment, capability/privacy eligibility, reason, quote/allowance evidence and expected cost components.
- Config: additive schema 1 optimization block with versioned policy, local opt-in and explicit privacy/spend controls. Existing explicit promptCache/off and automaticRoutes settings win. Missing values resolve to safe v3 policy; nullable policy fields distinguish absent from explicit false. No automatic rewriting of user config. Rollback restores the original backed-up config; document that old binaries ignore new policy fields and must not be used to enforce v3 restrictions.

Red: D5 account-specific prices; currency mismatch; unknown output price; expired credit; paid overage; quota used by concurrent requests; clone/merge/reload and explicit false preservation; old config readability.
Green: checked cost arithmetic, atomic local quota reservations reconciled from published usage, immutable per-request config snapshots. Unknown or externally shared credit cannot prove free-only safety; require provider-enforced no-charge entitlement or exclude.
Proof: `go test ./internal/economics ./internal/catalog ./internal/quota ./internal/config ./internal/compatdata`.

## T4 — grouped onboarding and official provider expansion

Order: after T3. Provider/doc work is parallel-safe with T5 only when each owns separate files; UI/server integration serial.
Files: internal/adapters/presets.go, presets_test.go, register.go; internal/adapter/hosted/hosted.go, hosted_test.go; internal/adapter/openai_compat/openai_compat.go, internal/ui/web/app.js, internal/ui/web/index.html, internal/server/server.go, internal/cli/accounts.go; docs/PROVIDERS.md, docs/OAUTH.md; docs/research/2026-10-05-v3-provider-evidence.md (new).

Inputs: #113–#115; economics and live catalog. Output: grouped presets and per-provider evidence. Keep OpenAI, Anthropic, OpenRouter, Zen/Go and OAuth special behavior intact; groups are presentation, not adapter renaming. Hide dead Qwen/Factory stubs from new-account choices without deleting saved entries or secrets.

First wave: Alibaba Coding Plan, DeepSeek, Mistral, Z.AI, MiniMax, Together, Fireworks, Cohere; Kilo, Vercel AI Gateway, SiliconFlow and MLX Serve. Verify official endpoint, auth, model discovery, wire/tools/stream behavior and terms separately for each. A provider lacking verified behavior is listed as unavailable/pending verification, not fabricated as working. Do not guess or hardcode model catalogs when discovery is unavailable.
Second wave review: OVHcloud, Aion Labs, ModelScope and Pollinations; ship only verified routes, otherwise record exact exclusion reason. Re-audit existing Cerebras trial, Hugging Face credit, Cloudflare allowance, Gemini privacy, NVIDIA trial, SambaNova, Ollama Cloud, OpenRouter and Zen entitlements.
Evidence sources: https://kilo.ai/docs/gateway/authentication ; https://vercel.com/docs/ai-gateway/pricing ; https://docs.cohere.com/docs/rate-limits ; https://mistral.ai/pricing ; https://docs.siliconflow.com/en/faqs/misc_rate ; https://inference-docs.cerebras.ai/support/pricing ; https://huggingface.co/docs/inference-providers/pricing ; https://developers.cloudflare.com/workers-ai/platform/pricing/ .

Red: group rendering, adapter preservation, stub exclusion with old-config preservation, missing key/account ID, anonymous explicit consent, live discovery, tools refusal, rate limits and correct streaming fixtures per new endpoint. No network in unit tests.
Green: thin adapters where genuinely compatible; provider-specific translation only when documented and tested. Add metadata provenance/expiry, not permanent quota constants. Never auto-spend or create accounts.
Proof: `go test ./internal/adapters ./internal/adapter/... ./internal/cli ./internal/server ./internal/config`; T11 UI test covers groups. Authorized live smoke records supported and failed features without claiming untested providers passed.

## T5 — disposition of subscription suggestions

Order: after scope approval; parallel-safe documentation-only investigation while T4 runs.
Files: docs/research/2026-10-05-v3-subscription-decisions.md (new), docs/OAUTH.md (edit serially after T4).
Input: #112/#116/#117. Output: evidence-backed decisions on authentication legitimacy, protocol/tool fidelity, available quota, licensing and ToS risk.

Kiro: bounded source/documentation review only. Acceptance for recommending a later adapter requires reproducible authorized auth/refresh, streaming and tool semantics, safe secret handling and explicit product approval. No adapter commitment is hidden in this plan. Devin stays deferred and Cursor session scraping stays excluded unless the user explicitly changes those decisions. No account scraping or paid probing.
Proof: `git diff --check`; checklist maps every issue to a decision and cited evidence. Remote updates require separate publication approval.

### T8 delivered — local assistant

`internal/localassistant` exists, is opt-in, and is reachable from the gateway
via `Gateway.LocalAssistant()`. What it deliberately does **not** do yet is
call a model: the readiness probe is built and tested, and no helper inference
is wired to it.

The safety envelope is the deliverable, and it is enforced at three layers:

- **Construction.** A non-loopback endpoint is refused with `ErrNotLoopback`,
  not warned about. A helper described as local that can be pointed at a remote
  host is not one.
- **Config load.** `optimization.localAssistantEndpoint` is validated when the
  file is read, so a typo or a remote address is reported at startup rather than
  the first time a prompt is sent. An endpoint without `localAssistant: true`
  is an error, not a silently ignored block.
- **Availability.** A disabled or unusable assistant is `nil`, and callers
  carry on. Nothing is downloaded, nothing is started, and there is no remote
  fallback: if the local endpoint is not running, the feature is simply absent.

Readiness requires a JSON model list, not merely a 200. Something else listening
on the port must not receive prompts.

Still open: the first real helper inference (routing classification and
retrieval ranking) is T10 work and is not started. `Assistant.Model()` is also
unimplemented beyond the configured value, which the first caller will need.
