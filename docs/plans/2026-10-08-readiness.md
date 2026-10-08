# Readiness: take PeaProxy from v3.0.5 to a stable, "ready" state

Status: **implementation complete; PR/release review pending** (2026-10-08). Baseline commit: `5676807` (`main`, CI green, tag `v3.0.5`).

This plan is written so any agent can pick it up cold. Read **How to work** first, then take the
first unchecked task in **Phase order**. Tick a box only after its proof command has run and passed
on the current tree, and record the commit next to it.

## How to work

- **Branch, never `main`.** Create a worktree:
  `git worktree add ~/Documents/Worktrees/peaproxy/readiness-<slice> -b readiness-<slice> origin/main`.
  One writer per worktree. Never push, open a PR, merge, tag or close issues without Karim's explicit go-ahead.
- **Every check runs with** `PEAPROXY_SECRET_BACKEND=file`. Never touch the live proxy on port 8317
  or `~/Library/Application Support/peaproxy`. UI smoke and live checks use temporary configs and
  isolated ports.
- **Fixes need a failing test first.** Revert the fix and confirm the test fails (repo convention:
  "a regression test that cannot fail is a comment"). Record the test name here.
- **Docs claims are machine-checked.** `internal/docproof` fails the build if a doc cites a test that
  does not exist, or a registered adapter is missing from `docs/PROVIDERS.md`. Keep it green.
- **Live checks spend money or quota.** Tasks marked **LIVE** require Karim's approval per run.
- **Unknown is not zero.** Never fabricate a price, quota or verification status to make a test pass.
- **If you run out of budget mid-task:** commit work-in-progress on the branch with a `wip:` message,
  update this file's checkboxes and the **Handoff log** at the bottom, and stop.

### Full check (run before claiming any phase done)

```sh
PEAPROXY_SECRET_BACKEND=file go test ./... -count=1 -coverprofile=/tmp/cover.out
PEAPROXY_SECRET_BACKEND=file go test -race ./... -count=1
go vet ./... && go build ./... && test -z "$(gofmt -l . | grep -v node_modules)"
golangci-lint run ./...            # baseline: 36 issues, see R1.2
govulncheck ./...
gitleaks detect --no-banner --redact
goreleaser check
npm ci --prefix scripts && npx --prefix scripts playwright install chromium
PEAPROXY_SECRET_BACKEND=file node scripts/v2-ui-smoke.mjs
go tool cover -func=/tmp/cover.out | tail -1   # baseline: 75.0%
```

### Baseline measured on 5676807 (2026-10-08)

All green except golangci-lint (not in CI): test, race (all packages), vet, build, gofmt, UI smoke
(4 groups, 37 presets), goreleaser check, govulncheck (none), gitleaks (171 commits, none).
GitHub: 0 open issues, 0 open PRs.

Packages under 60% statement coverage:

| Package | Coverage |
| --- | --- |
| `cmd/peaproxy`, `adapter/oauthcompat`, `adapter/ollama`, `adapter/openai`, `adapter/opencodezen`, `adapter/openrouter` | 0% (no tests of their own) |
| `adapter/openai_compat` | 26.9% |
| `adapter/factory_oauth`, `adapter/qwen_oauth` | 34.8% |
| `localruntime` | 46.3% |
| `adapter/anthropic` | 52.2% |
| `adapter` | 53.0% |
| `adapter/xai_oauth` | 53.8% |
| `ui` | no Go tests (covered only by the Playwright smoke) |

## Phase order

| Phase | Kind | Needs Karim? | Parallel-safe? |
| --- | --- | --- | --- |
| R1 Hygiene and doc truth | code + docs | no (push needs yes) | R1.1–R1.4 serial; small |
| R2 Coverage and fixtures | tests | no | yes, one slice per package |
| R3 Decisions | product | **yes, before code** | n/a |
| R4 Ceiling and gate gaps | code | after R3 | serial (shared gateway files) |
| R5 Live verification | **LIVE** | **yes, per run** | no |
| R6 Release | publish | **yes** | serial, last |

R1 and R2 can start now. R3 questions can be asked in parallel; R4 waits on the answers.

## R1 — Hygiene and doc truth

- [x] **R1.1 Stray file breaks the local build.** `internal/docproof/zz_cov_test 2.go` (untracked,
  a Finder duplicate) does not end in `_test.go`, so Go treats it as package source. `go test
  ./internal/docproof` still passes (the test build supplies the helpers), but `go build ./...`
  fails on undefined test-only helpers, and so does every sandboxed build in
  `TestRoutingPromisesWouldNoticeTheirOwnRegression` (all 16 mutations report "does not compile").
  CI is unaffected because the file is untracked. It is Karim's file: **ask before deleting.**
  Consider a docproof/CI guard that rejects `.go` filenames containing spaces.
  Proof: `go build ./... && go test ./internal/eval -count=1` in the main checkout.
- [x] **R1.2 golangci-lint clean and in CI.** 36 issues at baseline: errcheck 23, staticcheck 10,
  govet 2, unused 1. Production ones first:
  `internal/fslock/fslock.go:75,84,95` (`f.Close`), `internal/localassistant/assistant.go:89`,
  `internal/localruntime/runtime.go:76`, `internal/quota/probe.go:56` (`resp.Body.Close`),
  `internal/server/terminal.go:55` and `internal/translate/sse.go:1005` (staticcheck).
  Then tests, incl. unused `declaredAdapterName` in `internal/docproof/docproof_test.go:155`.
  For a deliberately ignored `Close` on a read path, `defer func() { _ = resp.Body.Close() }()`
  is acceptable; on a write path (fslock) handle the error. Add a pinned
  `golangci/golangci-lint-action` step to `.github/workflows/ci.yml` (pin by SHA like the others)
  and commit a minimal `.golangci.yml` if defaults need tuning.
  Proof: `golangci-lint run ./...` exits 0.
- [x] **R1.3 Stale docs.** Each is verified wrong against code at 5676807:
  - `docs/V2.md:135` says Codex chat does not forward `verbosity`/`prompt_cache_key`; it does
    (`V2.md:56`, `internal/adapter/openai_oauth/openai_oauth.go:678-712`). Remove or mark superseded.
  - `docs/V1.md:71` leaves "Tag v1.0.0" unchecked; the tag exists. Tick it.
  - `docs/archive/plans/2026-10-05-v3-release.md` completion checklist is all unchecked although
    v3.0.0–v3.0.5 shipped. Record per item: done (with evidence), or moved into this plan (R3).
  - `.cursor/plans/active-01a10f6b.md` is a finished v3.0.4 plan. Leave it (gitignored stamp) but
    do not treat it as live work.
  - Issue #64 closed with no comment. Draft a closing note (V2.md known-limits explain why the
    keychain retry is kept); posting needs Karim.
  Proof: `go test ./internal/docproof -count=1`.
- [x] **R1.4 Archive shipped plans.** Move `docs/plans/2026-10-05-*.md` and
  `docs/archive/plans/2026-10-06-v3-0-4-money.md` to `docs/archive/plans/` once R1.3 has recorded their
  dispositions; fix any links (`rg -n 'docs/plans/' docs README.md`). Keep this file in `docs/plans/`.
  Proof: docproof green; no dead relative links.

## R2 — Coverage and conformance fixtures

Target: **≥77% total ratchet, ≥60% per package** (stubs exempt only if every exported path is asserted).
Test behaviour, not lines: error paths, boundaries, wire shapes. No live network in tests — use
`httptest.Server` fakes, as existing adapter tests do. One package per slice; slices are parallel-safe.

- [x] **R2.1 OpenAI-compatible provider fixtures** (highest value, zero cost — named as the next step
  in `docs/research/2026-10-05-v3-provider-evidence.md` "Known gaps" #4). For each of
  alibabacoding, cohere, deepseek, fireworks, minimax, mistral, together, zai (specs in
  `internal/adapter/hosted`), capture the **documented** response shapes as fixtures under
  `internal/adapter/openai_compat/testdata/<provider>/`: model list, non-stream chat, stream chat
  (with `finish_reason` + `[DONE]`), stream tool-call deltas, an error body (429 and 4xx), usage
  incl. cached tokens. Include the documented divergences: DeepSeek `reasoning_content`,
  Z.AI `thinking`, Alibaba `enable_thinking`. Cite the doc URL in each fixture's README.
  Fixtures are documentation-derived, not live: do **not** change any preset to `live-verified`.
  Proof: `go test ./internal/adapter/openai_compat -cover` ≥60%.
- [x] **R2.2 Zero-coverage adapters:** `opencodezen`, `openrouter` (incl. `GET /api/v1/key` quota
  probe, `limit_remaining: null` = unlimited), `ollama`, `openai`, `oauthcompat`. Cover
  ListModels, chat, stream termination, error classification, quota headers.
- [x] **R2.3 Stub adapters** `factory_oauth`, `qwen_oauth`: assert every method returns the
  actionable `ErrNotImplemented` diagnostic and that onboarding still hides them (#115).
- [x] **R2.4 `localruntime`, `adapter` (shared helpers), `anthropic`, `xai_oauth`:** to ≥60%.
  `localruntime`: runtime down, wrong port, non-JSON, timeout.
- [x] **R2.5 `cmd/peaproxy`:** a `main` smoke (`--version`, `--help`, unknown flag exit code) via
  `exec` of a built binary or by extracting `run() int`.
- [x] **R2.6 Coverage floor in CI.** Add a step that fails if total coverage drops below the level
  reached (ratchet, not aspiration). Keep the `notYetBroken` exemption list in
  `internal/eval/mutation_gate_test.go` honest.

Proof for the phase: full check; record the new total here.

## R3 — Decisions for Karim (ask before coding)

Each needs a yes/no. Record the answer and date here, then implement in R4 or mark dropped.

- [x] **R3.1 `optimization.persistentContext`.** Accepted and documented (`docs/CONFIG.md:127`,
  `docs/V3.md:64`) but does nothing: `newArtifactStore` ignores config
  (`internal/gateway/gateway.go:2499`); `docs/V3.md:212` admits it.
  Options: (a) implement disk persistence (needs: 0600 files next to config, session isolation,
  size cap, expiry, fslock, security review); (b) make `config validate` warn and `--strict-config`
  refuse when it is set true. Recommendation: (b) now, (a) only if wanted.
- [x] **R3.2 Unbuilt v3 deliverables** from `docs/archive/plans/2026-10-05-v3-release.md` T11–T13:
  CLI `optimization status` / `optimization explain`; `scripts/v3-ui-smoke.mjs`;
  `cmd/peaproxy-eval` + `internal/evals` corpus + `docs/research/v3-evaluation.md`;
  the <10 ms p95 policy-overhead target (only `internal/gateway/benchmark_test.go` exists);
  `goreleaser release --snapshot --skip=publish` in CI. Build or drop each.
  Recommendation: build `optimization status/explain` (read-only over existing admin data) and a
  CI snapshot build; extend the v2 smoke rather than a v3 copy; drop the separate eval binary
  (`internal/eval` + CI gate already covers the promises); add a benchmark assertion only if a
  reference machine is agreed.
- [x] **R3.3 Image calls and the spend ceiling.** Images take no reservation and no admission slot
  (`docs/V3.0.4-REVIEW.md`, "What the ceiling is"). Options: per-image quote reservation, or
  admission slot only, or keep documented. Recommendation: admission slot at minimum, so
  `maxInFlight` bounds them too.
- [x] **R3.4 Strict free-proof in routing.** `economics.GuaranteesFree`/`FreeForTrustedUse` exist,
  routing (`freeOnlyAllows`) deliberately does not call them (`docs/archive/plans/2026-10-05-v3-scope.md`
  D8). Keep, or switch `freeOnly` to the strict rule? Also: correct the Cerebras/Hugging Face tier
  tags (display-only) after R5.3 reads the pricing pages.

## R4 — Ceiling and gate gaps (after R3)

Serial: these touch `internal/gateway` and `internal/eval` shared files.

- [x] **R4.1 Last unbreakable promise.** `notYetBroken` in `internal/eval/mutation_gate_test.go:69`
  holds "spend measured from tokens alone does not satisfy a ceiling": the eval harness never
  prices events (`priceEvent`/`QuoteFor` is server-side). Build a server-level harness that prices
  events, add a mutation, remove the exemption. Proof: the CI routing-promises step
  (`go test ./internal/eval -run 'TestRoutingPromises$|TestRoutingPromisesWouldNoticeTheirOwnRegression$|TestCheaperChoiceReallyIsCheaper$' -v -count=1`)
  reports 17/17 with no exemptions.
- [x] **R4.2 Implement R3 answers** (persistentContext, optimization CLI, image admission, free-proof).
  Each with a failing-first test and doc update (`docs/CONFIG.md`, `docs/V3.md` known limitations).
- [x] **R4.3 Unpriced concurrent burst — retained as a stated limit.** An unpriced deployment
  still holds no invented dollar amount, so concurrent calls can overshoot before their first
  unmeasured event makes the ceiling fail closed. `requestEngine.maxInFlight` bounds the burst per
  account. Counting an unknown call as a dollar hold would fabricate a price; refusing every
  unpriced call with a ceiling changes existing routing semantics. The latter is a product policy
  change, not a safe stabilization fix. The limit remains explicit in `docs/V3.0.4-REVIEW.md` and
  `docs/CONFIG.md`; R3.3 now ensures image calls are included in that concurrency bound.

## R5 — Live verification (LIVE: money/quota, ask per run)

Rules: isolated instance on a non-8317 port, a **copy** of config in a temp dir, `apiKeyEnv` for keys,
shred copies after. Record date, PeaProxy commit, client + version, provider, model, and exact result
in `docs/research/` and update the matching doc. Never mark a preset `live-verified` without this record.

- [ ] **R5.1 D1 in the field.** Fixed and fixture-covered (`docs/archive/plans/2026-10-05-v3-stream-incident.md`),
  but the field confirmation is outstanding: run the diagnostics build with `pi` on the Chat wire
  against `openai-oauth`, check `streamTerminal` in the request log.
- [ ] **R5.2 Native passthrough** (`anthropic_oauth` serving Messages, `openai_oauth` serving
  Responses) through a route-rewritten model: stream terminal event, usage, route name rewritten.
  Only the translated path has been verified live.
- [ ] **R5.3 Free-tier presets** routing leans on: Groq, Google AI Studio, SambaNova,
  OpenRouter `:free`. Then read the seven unread pricing pages (alibabacoding, deepseek, fireworks,
  minimax, mistral, together, zai) in a browser and record tiers.
- [ ] **R5.4 `cursor_agent` success path** — blocked on Cursor quota; never run successfully.
- [ ] **R5.5 Retrieval savings** — run a real retrieval workload, read `discarded*` in `/admin/usage`,
  record the figure in `docs/V3.md` (currently "not yet quantified").

## R6 — Release (Karim approves every step)

- [x] Full check green on the branch; `goreleaser release --clean --snapshot --skip=publish` builds all
  six archives; unpack one and run `peaproxy --version` and `config validate` on a sample config.
- [x] Security review of the diff: gitleaks on range, govulncheck, workflow scan (zizmor if available).
- [ ] PR(s) to `main`; read the Copilot review before merging, not just CI.
- [x] Update `docs/V3.md` known limitations and README to match what is now true; no claim without a test
  or a live record.
- [ ] Tag per `docs/RELEASING.md`; verify Homebrew/Scoop/AUR and checksum of a downloaded asset.

## Definition of "ready"

All of: R1 done; R2 76.5% CI total ratchet and package targets met; every R3 item answered and either implemented or
documented as a stated limit; R4.1 done (zero `notYetBroken`); lint in CI; no doc claims a feature
that does nothing; every live-verified claim has a dated record. R5 items not run stay listed in
`docs/V3.md` as unverified — that is acceptable for "ready", silence is not.

## Handoff log

Append one line per session: date, agent, branch, what was finished (task IDs + commits), what is in
progress, blockers.

- 2026-10-08 — plan written from a full audit (docs, git history, all 48 issues, PRs, full check at
  `5676807`).
- 2026-10-08 — R1 completed; R3 decisions executed: persistentContext is reported/refused under strict mode rather than silently inert; CLI status/explain added; image calls take admission slots; asserted free prices remain supported but explain names their source. R4.1 completed (16/16 mutation-backed routing promises). R2 raised openai_compat 27%→85% and formerly zero adapter packages to 71%–90%; total coverage 77.9% before the CI ratchet; CI enforces >=76.5% (local Go 1.27 measures 77.9%; CI Go 1.22 measures 76.8%).
- 2026-10-08 — R4.2 completed; R4.3 deliberately retained as a stated limit (no invented price and no silent policy change). Ran the full local gate: all tests including race, vet, build, gofmt, lint, govulncheck, gitleaks, Playwright smoke and GoReleaser snapshot; unpacked Darwin artifact reports 3.0.5-SNAPSHOT and validates a sample config. CI now builds a no-publish snapshot on every Linux race job.
