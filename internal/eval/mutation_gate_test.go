package eval

import (
	"bytes"
	"fmt"
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
	// Why states what this proves, so a failure here is legible.
	Why string
}

func routingMutations() []Mutation {
	return []Mutation{
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
	// Serial on purpose: this edits source files other packages are compiled
	// from, and two of these running at once would corrupt each other's view of
	// the tree. So no t.Parallel here, deliberately.

	root := repoRoot(t)
	mutations := routingMutations()

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
			if err := os.WriteFile(path, mutated, 0o644); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.WriteFile(path, before, 0o644); err != nil {
					t.Fatalf("restoring %s: %v", m.File, err)
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
