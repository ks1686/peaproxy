package cursoragent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
)

// fakeCLI installs an executable named cursor-agent on PATH for the duration of
// the test. The adapter shells out to a real binary, so testing it any other
// way would prove nothing about the thing most likely to break: the boundary
// with a subprocess.
//
// It is POSIX-only. cursor-agent is itself a shell script, and so is the fake;
// on Windows LookPath finds neither an extensionless shell script nor runs it,
// and the whole suite fails there. A .bat fake would exercise batch quoting
// rather than the boundary under test, which is the opposite of the point.
func fakeCLI(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("cursor-agent is a POSIX CLI; the fake is a /bin/sh script, and a .bat fake " +
			"would test batch quoting rather than the subprocess boundary")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, Binary)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func newTestAdapter(t *testing.T, script string) adapter.Adapter {
	t.Helper()
	fakeCLI(t, script)
	a, err := New(adapter.Options{ID: "cursor-test"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Cursor's CLI prints the model list as text, with a header the adapter has to
// skip. If the header leaks through as a model, routing offers a model that
// does not exist.
func TestModelListingSkipsTheHeaderAndKeepsRealIDs(t *testing.T) {
	a := newTestAdapter(t, `cat <<'EOF'
Available models

auto - Auto (default)
gpt-5.3-codex - Codex 5.3
composer-2.5 - Composer 2.5
cursor-grok-4.5-high - Grok 4.5
EOF`)
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	want := []string{"auto", "gpt-5.3-codex", "composer-2.5", "cursor-grok-4.5-high"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("listed %v, want %v", ids, want)
	}
}

// An empty list is a failure, not an empty catalog. Reporting zero models makes
// the account look like it serves nothing, which is indistinguishable from
// being broken.
func TestAnEmptyModelListIsAFailureRatherThanAnEmptyCatalog(t *testing.T) {
	a := newTestAdapter(t, `printf 'Available models\n\n'`)
	if _, err := a.ListModels(context.Background()); err == nil {
		t.Fatal("an empty model list was reported as a catalog")
	}
}

// The CLI takes one prompt string, not a message array, so a conversation has to
// be flattened. Roles are the part that has to survive: an agent that cannot see
// which turn was the user's answers a different question.
func TestAConversationIsFlattenedWithItsRolesIntact(t *testing.T) {
	// Capture the last argument with a portable loop. ${!#} is a bash
	// indirect expansion, and fakeCLI writes a /bin/sh script: on a system
	// where /bin/sh is dash this fails with "bad substitution" before the
	// fake CLI ever answers, which looks like a broken adapter rather than a
	// broken test.
	dir := t.TempDir()
	out := filepath.Join(dir, "prompt.txt")
	fakeCLI(t, "last=\"\"; for a in \"$@\"; do last=\"$a\"; done; printf '%s' \"$last\" > "+out+"; echo '{\"result\":\"ok\"}'")

	a, err := New(adapter.Options{ID: "cursor-test"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Chat(context.Background(), adapter.ChatRequest{
		Model: "gpt-5.3-codex",
		Messages: []adapter.Message{
			{Role: "system", Content: "Be terse."},
			{Role: "user", Content: "first question"},
			{Role: "assistant", Content: "first answer"},
			{Role: "user", Content: "second question"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(b)
	for _, want := range []string{"Be terse.", "first question", "first answer", "second question"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the flattened prompt lost %q:\n%s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "User:") || !strings.Contains(prompt, "System instructions:") {
		t.Fatalf("the flattened prompt does not mark roles:\n%s", prompt)
	}
}

// Tool calls cannot be honoured: the CLI runs in --mode ask, which is read-only
// Q&A. Saying "unknown" here would let routing send a tool call to an upstream
// that structurally cannot answer one.
func TestToolsAreReportedUnsupportedRatherThanUnknown(t *testing.T) {
	fakeCLI(t, `echo '{"result":"ok"}'`)
	a, err := New(adapter.Options{ID: "cursor-test"})
	if err != nil {
		t.Fatal(err)
	}
	caps := a.Capabilities()
	if caps.Tools {
		t.Fatal("tools are reported as supported, but --mode ask is read-only Q&A")
	}
	if !caps.Chat || !caps.Stream || !caps.ListModels {
		t.Fatalf("the adapter does not claim what it does: %+v", caps)
	}
}

// The CLI reports a usage refusal on stderr, in prose. Replacing that with
// "exit status 1" throws away the only text an operator can act on.
func TestAQuotaRefusalIsReportedInTheOperatorsOwnWords(t *testing.T) {
	a := newTestAdapter(t, `echo "ActionRequiredError: Increase limits for faster responses You're out of usage. Switch to Auto, or ask your admin to increase your limit to continue." >&2
exit 1`)
	_, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model:    "gpt-5.3-codex",
		Messages: []adapter.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("a failed agent run was reported as a successful turn")
	}
	if !strings.Contains(err.Error(), "out of usage") {
		t.Fatalf("the refusal was replaced with %v; the operator's own words are the actionable part", err)
	}
}

// The trust banner is a prompt, not a refusal, and leading with it would tell
// the operator the wrong problem.
func TestTheTrustBannerDoesNotMasqueradeAsTheFailure(t *testing.T) {
	a := newTestAdapter(t, `cat >&2 <<'EOF'

⚠ Workspace Trust Required

  Cursor Agent can execute code and access files in this directory.
  Do you trust the contents of this directory?
EOF
echo "ActionRequiredError: You're out of usage." >&2
exit 1`)
	_, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model:    "gpt-5.3-codex",
		Messages: []adapter.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("a failed run was reported as a successful turn")
	}
	if strings.Contains(err.Error(), "Workspace Trust") {
		t.Fatalf("the trust banner is reported as the failure:\n%v", err)
	}
	if !strings.Contains(err.Error(), "out of usage") {
		t.Fatalf("the real refusal was not reported:\n%v", err)
	}
}

// A client that hangs up mid-stream is not the upstream's error, so a failed
// write must not be reported as one.
func TestAStreamedRunDoesNotFailBecauseTheClientWentAway(t *testing.T) {
	a := newTestAdapter(t, `printf '{"type":"text","text":"partial"}\n'
echo '{"result":"done"}'`)
	var sink brokenWriter
	err := a.ChatStream(context.Background(), adapter.ChatRequest{
		Model:    "gpt-5.3-codex",
		Messages: []adapter.Message{{Role: "user", Content: "hi"}},
	}, sink)
	if err != nil {
		t.Fatalf("a hung-up client failed the upstream run: %v", err)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write(p []byte) (int, error) { return 0, os.ErrClosed }

// An account whose CLI is missing cannot ever work, and says so at validation
// rather than failing every call with an exec error later.
func TestAMissingCLIIsRefusedAtConstruction(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := New(adapter.Options{ID: "cursor-test"}); err == nil {
		t.Fatal("an adapter was built with no cursor-agent on PATH")
	}
}

var _ = bytes.MinRead
