# Provider evidence — v3

This page records what PeaProxy has actually established about each provider, and
how. It exists because the v3 product leans harder than anything shipped so far
on claims about who is free, what something costs, and whether an endpoint
works. A router that sends a bill to a paid provider because a table said
"free" is worse than no router.

Nothing here is a promise to the user. It is an audit trail, including of the
places where the audit is incomplete.

Read [PROVIDERS.md](../PROVIDERS.md) for the adapter tier map. Read
[../archive/plans/2026-10-05-v3-scope.md](../archive/plans/2026-10-05-v3-scope.md) for the
issue dispositions this feeds.

## How to read the tiers

| Tier | Meaning | What it licenses |
|---|---|---|
| **live-verified** | A real key exists and PeaProxy completed a call | PeaProxy may claim the provider works |
| **conformance-verified** | PeaProxy's adapter is exercised against the provider's documented wire format using fixtures | PeaProxy may claim the wire format is handled. **Not** that the live endpoint behaves that way |
| **documented-only** | Base URL and limits come from the provider's own documentation, read but not exercised | PeaProxy may claim only what the documentation states |

No tier above `documented-only` is currently held for any metered provider.
This is a real limitation and is stated rather than papered over.

### Why conformance fixtures are per-provider

All eight new key presets share the `openai_compat` adapter. That does not make
them one integration. OpenAI-compatibility is a family resemblance:

- DeepSeek returns `reasoning_content` alongside `content`, with a documented
  string-or-null contract.
- GLM and Z.AI emit a `thinking` block in the delta stream.
- Alibaba Coding Plan takes `enable_thinking` and has no reasoning field at all.

A working Groq key tells us nothing about whether PeaProxy parses
`reasoning_content`. Inheriting "known solutions from other providers" would
produce eight presets that look verified and quietly mishandle streaming. So
conformance fixtures are written per provider.

## Verification method used in this pass

For each provider: read the provider's own API documentation — endpoint path,
request shape, response envelope, usage block, streaming frames, error shape —
and record the URL. Do not infer an endpoint from a third-party integration
guide, a blog post, or a similar provider.

Where a page is JavaScript-rendered and could not be read as text, that is
recorded as **not read**, and no claim is derived from it.
## Existing "freemium" claims, re-audited

v2 tags several providers `freemium`. That tag drives automatic routing, so it
was re-checked rather than inherited. Two claims did not survive.

**No tag was changed by this pass.** The findings below are from the v2
research pass and have not been re-verified in this session, so the code still
carries the v2 tags. That is recorded as defect D8 rather than quietly patched.

| Provider | v2 tag (unchanged) | Finding | Status |
|---|---|---|---|
| Cerebras | freemium | The $5 balance is a **one-time promotional credit**, not a recurring allowance | **D8**, unverified this pass |
| Hugging Face | freemium | Free allowance is **$0.10/month** — a rounding error, not a free tier | **D8**, unverified this pass |
| Cloudflare Workers AI | freemium | 10,000 neurons/day, a real allowance, but the unit is neurons, not tokens, and does not map to a per-token price | Keep; unit recorded |
| Google AI Studio | freemium | Free tier is real, but prompts on the free tier are **used to improve Google products** | Keep; surface as a warning, not a silent free route |
| OpenRouter | freemium | Free models are the `:free` **variants** only; the paid route through OpenRouter is not free | Keep; `:free` detection stays suffix-based and narrow |
| Groq, SambaNova, NVIDIA NIM | freemium | Documented free allowances from the v2 research pass | `documented-only`; not live-verified |

The pattern worth naming: **a promotional credit is not an allowance.** A
one-time $5 that expires in 30 days and a recurring monthly free tier look
identical in a config file and behave completely differently at month two. Any
allowance PeaProxy reports has to say which kind it is, and a tier tag must
never be read as proof that a call will be free.

Leaving the tags alone is the safe half of this. Retagging them on research
this session could not re-verify would be the same error in the opposite
direction — swapping one unverified claim for another.

## The eight new key presets (#114)

All `documented-only`. None has been called live. Base URLs were read from
provider documentation; the free-tier question was **not** answered for any of
them, and that is the open item below.

| Preset | Adapter | Base URL | Tier | Free tier |
|---|---|---|---|---|
| `alibaba-coding-plan` | `alibabacoding` | `https://coding-intl.dashscope.aliyuncs.com/v1` | paid | **not read** |
| `deepseek-key` | `deepseek` | `https://api.deepseek.com/v1` | paid | **not read** |
| `mistral-key` | `mistral` | `https://api.mistral.ai/v1` | paid | trial noted, terms unread |
| `zai-key` | `zai` | `https://api.z.ai/api/paas/v4` | paid | **not read** |
| `minimax-key` | `minimax` | `https://api.minimax.io/v1` | paid | **not read** |
| `together-key` | `together` | `https://api.together.xyz/v1` | paid | **not read** |
| `fireworks-key` | `fireworks` | `https://api.fireworks.ai/inference/v1` | paid | **not read** |
| `cohere-key` | `cohere` | `https://api.cohere.ai/compatibility/v1` | paid | trial: 1,000 calls/month |

Every one of these presets renders in the UI as `(not verified)`.

## Known gaps

Stated plainly, because the release notes must not imply these are closed.

1. **No metered provider is live-verified.** Eight new presets are
   `documented-only`. Every one of them shows `(not verified)` in the UI.
2. **The free tier for seven of the eight new presets was never read.** The
   pricing pages are JavaScript-rendered and the text fetch returned
   application shell. The base URLs came from documentation; the free-tier
   question did not, and no tier was assigned from inference.
3. **Cerebras and Hugging Face tags are unverified and probably wrong.** See
   D8 in [../archive/plans/2026-10-05-v3-scope.md](../archive/plans/2026-10-05-v3-scope.md).
   Not corrected in this pass, deliberately. Worth being precise about the
   impact: the router never reads the `freemium` tag, so this is a display
   accuracy problem, not a routing one. The routing defect in D8 is the
   price-based free proof, and it is reachable through a user-configured
   `0/0` override rather than through these tags.
4. **No conformance fixtures exist yet for any of the eight.** The tier exists
   on paper. Reaching it means capturing each provider's documented response
   and stream shape as fixtures under `internal/adapter/openai_compat`, and is
   the highest-value next step that requires no spending.
5. **Cache pricing is parsed only from OpenRouter.** Every other provider's
   cache read/write rates remain unknown, so a strict free-route check cannot
   pass for them. Unknown is correct here; guessed is not.

## How to close the gaps

Cheapest first, most valuable first:

1. **Conformance fixtures per provider.** No credential, no cost, catches the
   real parsing risk.
2. **Sign up for Cohere** — 1,000 calls/month on a trial key. It is the only
   new preset with a documented free trial, so it is the cheapest way to reach
   `live-verified` and to validate the whole `openai_compat` path end to end.
3. **Read the seven unread pricing pages** in a browser and record the answer
   here. Turns six "not read" cells into facts.
4. **Live-verify the existing freemium set** — Groq, Google AI Studio,
   SambaNova, OpenRouter `:free` — before v3 ships, since automatic routing
   leans on those tags harder than v2 ever did.

Steps 1 and 3 cost nothing. They should happen before, not after, release.

## Cohere — live-verified 2026-10-05

Upgraded from `documented-only` to `live-verified`. Verified against a real
trial account, through PeaProxy's own adapter path on an isolated instance
(port 8931, own config directory, `apiKeyEnv` so the key never touched a file).
The live proxy on 8317 was not involved.

| Check | Result |
| --- | --- |
| `GET /models` discovery | 35 models returned; `command-r7b-12-2024` present |
| Non-streaming chat | `finish_reason: stop`, content and usage returned |
| Streaming chat | `finish_reason` present, `[DONE]` sentinel present |
| Streaming with `tools` | `tool_calls` deltas emitted, terminal finish event emitted |
| Usage reporting | `prompt_tokens`, `completion_tokens`, `prompt_tokens_details.cached_tokens` present |

This is the first provider verified against a live account. It does not make the
other 18 presets verified, and the marker stays on all of them.

Note the D1 relevance: Cohere's OpenAI-compatible endpoint terminates its stream
correctly, which is the behaviour the `openai_compat` adapter is now tested
against. That test was written before this verification and passed against a
mock; a live account agreeing with it is corroboration, not proof.

### Trial allowance

The trial key provides a bounded monthly call allowance. The exact number was
taken from Cohere's documentation and **not** measured, because measuring it
would mean spending it. It stays classified as a finite trial rather than a
recurring free tier, which is the conservative reading.
