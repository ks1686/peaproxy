package eval

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A routing promise that nobody has tried to break is a comment.
//
// This gate breaks each one on purpose and insists the scenario notices. It is
// slow, it edits the working tree while it runs, and it is skipped under -short;
// in exchange it answers the only question a gate can be asked: if the code
// under it went back to being wrong, would anything here say so?
//
// The mechanics matter, because the naive version of this test is worthless.
// Editing a source file while the test binary that would exercise it is already
// compiled changes nothing at all: the mutation has to be recompiled, which
// means a subprocess, and a scenario that fails to compile has proved nothing
// either, so that case is rejected rather than counted as a pass.
type Mutation struct {
	// Scenario is the promise, by its exact name.
	Scenario string
	// File is module-relative: internal/catalog/pricing.go
	File string
	// From must appear exactly once in File. Exactly once, because "no match"
	// means the code has drifted and the mutation stopped being a mutation,
	// and silently passing on zero matches is how a gate becomes a no-op.
	From string
	To   string
	// AlsoFrom/AlsoTo break a second guard in AlsoFile, for a promise that
	// depends on two of them stacked. A single edit can leave the other one
	// standing, and the scenario then passes with its protection half intact --
	// which reads as "the promise is covered" and is not.
	AlsoFile string
	AlsoFrom string
	AlsoTo   string
	// Why states what this proves, so a failure here is legible.
	Why string
}

// assertEveryPromiseIsBroken fails when a routing promise has no mutation, or a
// mutation names a promise that no longer exists.
//
// A promise nobody tries to break is a comment. The previous version of this
// gate covered six of fifteen scenarios and the README said "each promise", so
// the gap read as coverage from the outside -- which is the failure this file
// exists to prevent, applied to the file itself.
// notYetBroken lists promises that are asserted every run but have no mutation
// yet, each with the reason it could not be written.
//
// They are named rather than omitted. The previous version of this gate left
// nine of fifteen promises unmutated and said nothing, so the gap read as
// coverage; an explicit list with a reason is a debt someone can close, and it
// cannot grow by accident.
var notYetBroken = map[string]string{
	"spend measured from tokens alone does not satisfy a ceiling": "structurally unbreakable here, not weakly tested: pricing an event is the server's job " +
		"(priceEvent calls QuoteFor), and the eval harness never does it, so no event in a " +
		"scenario can ever gain a CostUSD and tokens alone cannot become cost. Both halves of the " +
		"promise are covered elsewhere -- the parser by TestPartialUsageIsNotPriced in internal/usage, " +
		"and the priced-versus-total accounting by the mutation on w.Priced above. Making it " +
		"breakable needs a harness that prices events, which is a server-level test",
	"an unknown price never wins over a known one": "three guards stand in the way -- automaticKind filters an unpriced deployment out of economy " +
		"candidacy, cheapest skips it while ranking, and Cheaper refuses both an unpriced challenger and " +
		"an unpriced incumbent. Breaking the first two still leaves the promise intact, which is defence " +
		"in depth rather than a weak promise; showing it would need all three broken at once",
}

func assertEveryPromiseIsBroken(t *testing.T, mutations []Mutation) {
	t.Helper()
	scenarios := routingPromises()
	mutated := map[string]bool{}
	for _, m := range mutations {
		if mutated[m.Scenario] {
			t.Errorf("two mutations target the same promise %q; one of them proves nothing extra", m.Scenario)
		}
		mutated[m.Scenario] = true
	}
	for _, s := range scenarios {
		if mutated[s.Name] {
			continue
		}
		if reason, exempt := notYetBroken[s.Name]; exempt {
			t.Logf("promise %q is asserted but not yet broken: %s", s.Name, reason)
			continue
		}
		t.Errorf("promise %q has no mutation and no recorded reason for its absence: it is asserted, "+
			"but nothing checks that it can fail. Either add one to routingMutations, or record the reason "+
			"in notYetBroken.", s.Name)
	}
	for _, m := range mutations {
		found := false
		for _, s := range scenarios {
			if s.Name == m.Scenario {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("mutation targets %q, which is not a routing promise any more", m.Scenario)
		}
	}
}

func routingMutations() []Mutation {
	return []Mutation{
		{
			Scenario: "an explicitly selected model is never substituted",
			File:     "internal/gateway/engine_route.go",
			From:     "if !router.Automatic(model) {",
			To:       "if !(!router.Automatic(model)) {",
			Why: "routing a model the client named as if it were a route name lets a cheaper " +
				"deployment answer it, which is silent degradation dressed as a saving",
		},
		{
			Scenario: "economy prefers the cheapest deployment",
			File:     "internal/gateway/engine_route.go",
			From:     "best := -1",
			To:       "return 0\n\tbest := -1",
			Why:      "ranking has to pick the cheapest, not the first one declared",
		},
		{
			Scenario: "free-only still serves a free deployment",
			File:     "internal/gateway/engine_route.go",
			From:     "return p.Verified && p.Currency == economics.LedgerCurrency && p.Free()",
			To:       "return p.Verified && false",
			Why:      "a deployment proven free must survive free-only; blocking everything would not protect the account, it would break it",
		},
		{
			Scenario: "an unmeasured spend total does not satisfy a ceiling",
			File:     "internal/usage/usage.go",
			From:     "w.Priced += d.CostCalls + d.EstimatedCalls",
			To:       "w.Priced += d.Calls",
			Why:      "counting every call as priced turns a fail-closed ceiling into a fail-open one",
		},
		{
			Scenario: "free-only refuses a named model too",
			File:     "internal/gateway/engine_route.go",
			From:     "if g.cfg.FreeOnly() && !deploymentProvenFree(g, m) {",
			To:       "if false {",
			Why:      "naming a model must not be a way around the switch that protects the account",
		},
		{
			Scenario: "the caller's own tools survive routing",
			File:     "internal/contextopt/inject.go",
			From:     "tools = append(tools, spec)",
			To:       "tools = []json.RawMessage{spec}",
			Why: "injecting PeaProxy's own tool by replacing the caller's is the same as removing " +
				"what the client asked for",
		},
		{
			Scenario: "warmth decides when price cannot",
			File:     "internal/gateway/cache_warm.go",
			From:     "return catalog.NotDearer(warm, current)",
			To:       "return false",
			Why:      "warmth is the tie-breaker when price leaves two deployments equal; declining always is not a tie-break",
		},
		{
			Scenario: "economy weighs output, not only input",
			File:     "internal/catalog/pricing.go",
			From:     "return blendInputWeight*ai+blendOutputWeight*ao < blendInputWeight*bi+blendOutputWeight*bo",
			To:       "return ai < bi",
			Why:      "crossed rates are exactly the case this blend exists for; without it an input-only comparison sends the expensive answer",
		},
		{
			Scenario: "free-only refuses a metered deployment",
			File:     "internal/gateway/engine_route.go",
			From:     "if g.cfg.FreeOnly() {",
			To:       "if false {",
			Why:      "free-only exists to refuse spend; a deployment that cannot prove it is free must be refused",
		},
		{
			Scenario: "the spend ceiling reaches a named model too",
			File:     "internal/gateway/spend_ceiling.go",
			From:     "func (g *Gateway) ceilingBlocksIn(w usage.SpendWindow, ceiling, pending float64) (bool, string) {",
			To:       "func (g *Gateway) ceilingBlocksIn(w usage.SpendWindow, ceiling, pending float64) (bool, string) {\n\t_ = w\n\t_ = ceiling\n\t_ = pending\n\treturn false, \"\"",
			Why:      "a ceiling that stops blocking stops being a ceiling, and naming a model is not a way around it",
		},
		{
			Scenario: "a route refuses a provider with no account attached",
			File:     "internal/gateway/engine_route.go",
			From:     "if !g.cfg.AllowAnonymousProviders() && !g.runsOnThisMachine(m.AccountID, m.ID) && anonymousDeployment(g, m) {",
			To:       "if false {",
			Why:      "the consent guard is the only thing standing between a prompt and somebody else's machine",
		},
		{
			Scenario: "a request needing tools is not served by a tool-less deployment",
			File:     "internal/gateway/engine_route.go",
			From:     "if !eligibleForAutomaticRoute(evidenceFor(inst), req.Requirements) {",
			To:       "if false {",
			Why:      "a tool call sent to an endpoint that cannot run it is lost work; this regressed as D9",
		},
		{
			Scenario: "warmth never beats a cheaper deployment",
			File:     "internal/gateway/cache_warm.go",
			From:     "return catalog.NotDearer(warm, current)",
			To:       "if warm.Input == nil || current.Input == nil {\n\t\treturn false\n\t}\n\treturn *warm.Input <= *current.Input",
			Why:      "warmth is a tie-breaker; judging it on the input rate alone reinstates the cheaper-output trap",
		},
	}
}

// repoRoot walks up from the test's working directory to the module root, so the
// harness works from a worktree as readily as from a checkout.
// sandbox copies the working tree so the mutations never touch the real one.
//
// This was a live bug: `go test ./...` compiles every package in parallel, so
// the eval package rewriting internal/usage/usage.go on disk meant the usage
// package could compile the mutant instead of the source. On CI it did, and five
// tests in internal/usage failed on a branch where every one of them passes
// locally -- a failure that looked like a regression in the spend ledger and
// was nothing of the kind. Serialising this test does not help; the other
// packages are compiled by `go test`, not by anything under this test's
// control.
//
// The copy is of the working tree, not of HEAD, so a developer gets a result
// about the code they are editing rather than about the last commit.
func sandbox(t *testing.T) string {
	t.Helper()
	src := repoRoot(t)
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		// .git is large and irrelevant; node_modules is not Go source.
		if d.IsDir() {
			if rel == ".git" || rel == "node_modules" || strings.HasPrefix(rel, "scripts/node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		// Everything else comes too, because //go:embed needs it: an embedded
		// asset left out of the copy fails the build, and a sandbox that cannot
		// build proves nothing about the mutation.
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatalf("copying the tree into a sandbox: %v", err)
	}
	return dst
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod above the working directory")
		}
		dir = parent
	}
}

// subtestPattern selects exactly one scenario. Go replaces spaces in subtest
// names with underscores, which is why the pattern is not the plain name.
func subtestPattern(name string) string {
	return "^TestRoutingPromises$/" + regexp.QuoteMeta(strings.ReplaceAll(name, " ", "_")) + "$"
}

func TestRoutingPromisesWouldNoticeTheirOwnRegression(t *testing.T) {
	if testing.Short() {
		t.Skip("mutation gate recompiles the tree per scenario; skipped under -short")
	}
	root := sandbox(t)
	mutations := routingMutations()

	// The coverage claim is the whole point of this file, so it is asserted
	// rather than left to be noticed. Six mutations against fifteen promises
	// once read as complete, because nothing compared the two sets.
	assertEveryPromiseIsBroken(t, mutations)

	// Guard the other direction first: the promises pass before anything is
	// touched, so a failure below means the mutation was caught rather than the
	// gate simply being broken.
	if out, err := goRun(root, "test", "./internal/eval/", "-run", "^TestRoutingPromises$", "-count=1"); err != nil {
		t.Fatalf("the gate does not pass before mutation:\n%s\n%v", out, err)
	}

	// Read every file before touching any of them, and restore them all if
	// anything below goes wrong: an interrupted run must not leave the tree
	// broken.
	original := map[string][]byte{}
	restored := make([]string, 0, len(mutations))
	for _, m := range mutations {
		if _, seen := original[m.File]; seen {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, m.File))
		if err != nil {
			t.Fatalf("reading %s: %v", m.File, err)
		}
		original[m.File] = b
		restored = append(restored, m.File)
		if m.AlsoFile != "" && m.AlsoFile != m.File {
			extra, err := os.ReadFile(filepath.Join(root, m.AlsoFile))
			if err != nil {
				t.Fatalf("reading %s: %v", m.AlsoFile, err)
			}
			original[m.AlsoFile] = extra
			restored = append(restored, m.AlsoFile)
		}
	}
	defer func() {
		for _, f := range restored {
			if err := os.WriteFile(filepath.Join(root, f), original[f], 0o644); err != nil {
				t.Errorf("restoring %s: %v", f, err)
			}
		}
	}()

	for _, m := range mutations {
		m := m
		t.Run(m.Scenario, func(t *testing.T) {
			path := filepath.Join(root, m.File)
			before := original[m.File]

			if n := bytes.Count(before, []byte(m.From)); n != 1 {
				t.Fatalf("%s contains %d occurrences of the mutation anchor, want exactly 1.\n"+
					"The code this promise guards has moved or already changed; update the mutation "+
					"rather than letting this pass silently.\nanchor: %s", m.File, n, m.From)
			}

			mutated := bytes.Replace(before, []byte(m.From), []byte(m.To), 1)
			if bytes.Equal(mutated, before) {
				t.Fatalf("%s was not modified; the mutation was a no-op", m.File)
			}

			var alsoPath string
			var alsoBefore, alsoMutated []byte
			if m.AlsoFile != "" {
				if n := bytes.Count(mutated, []byte(m.AlsoFrom)); n != 1 {
					t.Fatalf("%s contains %d occurrences of the second anchor, want exactly 1:\nanchor: %s",
						m.AlsoFile, n, m.AlsoFrom)
				}
				alsoPath = filepath.Join(root, m.AlsoFile)
				b, rerr := os.ReadFile(alsoPath)
				if rerr != nil {
					t.Fatal(rerr)
				}
				alsoBefore = b
				alsoMutated = bytes.Replace(alsoBefore, []byte(m.AlsoFrom), []byte(m.AlsoTo), 1)
			}

			if err := os.WriteFile(path, mutated, 0o644); err != nil {
				t.Fatal(err)
			}
			if alsoPath != "" {
				if err := os.WriteFile(alsoPath, alsoMutated, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			defer func() {
				if err := os.WriteFile(path, before, 0o644); err != nil {
					t.Fatalf("restoring %s: %v", m.File, err)
				}
				if alsoPath != "" {
					if err := os.WriteFile(alsoPath, alsoBefore, 0o644); err != nil {
						t.Fatalf("restoring %s: %v", m.AlsoFile, err)
					}
				}
			}()

			// A mutation that does not compile proves nothing: the scenario
			// would "fail" because nothing ran.
			if out, err := goRun(root, "build", "./internal/..."); err != nil {
				t.Fatalf("the mutation does not compile, which is not a regression:\n%s\n%v", out, err)
			}

			out, err := goRun(root, "test", "./internal/eval/", "-run", subtestPattern(m.Scenario), "-count=1")
			if err == nil {
				t.Fatalf("the promise %q still passes with its guard broken.\n%s\n%s",
					m.Scenario, m.Why, out)
			}
			t.Logf("caught as promised: %s\n%s", m.Why, lastLines(out, 3))
		})
	}

	// And back to green: proof the tree was left as it was found.
	if out, err := goRun(root, "test", "./internal/eval/", "-run", "^TestRoutingPromises$", "-count=1"); err != nil {
		t.Fatalf("the gate does not pass again after every mutation was reverted:\n%s\n%v", out, err)
	}
}

// goRun runs a go command in the module root and returns its combined output.
func goRun(root string, args ...string) (string, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return fmt.Sprintf("    %s", strings.Join(lines, "\n    "))
}
