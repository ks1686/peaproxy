package cursoragent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
)

// The banner above is filtered by matching text Cursor chose to print today.
// That list is a guess about wording, and the next release will reword it.
//
// So the refusal has to be found by what it *is* -- an error -- rather than by
// skipping text it is hoped will not be there. This banner deliberately uses
// wording the filter knows nothing about; if only the filter were load-bearing,
// this test would report the banner as the failure.
func TestAnUnrecognisedBannerStillDoesNotHideTheRefusal(t *testing.T) {
	a := newTestAdapter(t, `cat >&2 <<'SCRIPT'
some entirely new preflight notice nobody has seen before
another unfamiliar line about sandboxing
ActionRequiredError: You are out of usage.
SCRIPT
exit 1`)
	_, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model:    "gpt-5.3-codex",
		Messages: []adapter.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("a failed run was reported as a successful turn")
	}
	if !strings.Contains(err.Error(), "out of usage") {
		t.Fatalf("an unfamiliar banner hid the real refusal:\n%v", err)
	}
}

// Validate answers "can this account ever work", which is knowable without
// spending a call. Building the adapter and checking the binary are not the same
// thing: the CLI can be uninstalled after the account is configured, and the
// check still has to keep answering.
func TestValidationReportsAMissingBinaryEvenAfterTheAdapterWasBuilt(t *testing.T) {
	fakeCLI(t, `echo ok`)
	a, err := New(adapter.Options{ID: "cursor-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Validate(context.Background()); err != nil {
		t.Fatalf("validation failed while the CLI was present: %v", err)
	}
	// The binary is gone now, but the adapter already exists.
	t.Setenv("PATH", t.TempDir())
	if err := a.Validate(context.Background()); err == nil {
		t.Fatal("validation passed with no cursor-agent on PATH: an account that can never " +
			"work was reported as sound")
	}
}

// An apiKey present in config must be reported as a key-backed account, and its
// absence must not be reported as one. PeaProxy's secret store and the operator's
// decisions both hang off that flag.
func TestAPIKeySupportIsReportedOnlyWhenAKeyIsConfigured(t *testing.T) {
	fakeCLI(t, `echo ok`)
	withKey, err := New(adapter.Options{ID: "c", APIKey: "cursor-key-abc"})
	if err != nil {
		t.Fatal(err)
	}
	if !withKey.Capabilities().APIKey {
		t.Fatal("an account configured with an API key does not report key support")
	}
	without, err := New(adapter.Options{ID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if without.Capabilities().APIKey {
		t.Fatal("an account with no key claims key support, so the secret store would offer to fill one in")
	}
}

// A run that produces no text is a failure. Returning an empty successful turn
// hands the client a blank answer and records it as a real call.
func TestAnEmptyAgentTurnIsAFailureRatherThanABlankAnswer(t *testing.T) {
	a := newTestAdapter(t, `echo '{"result":"   "}'`)
	_, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model:    "gpt-5.3-codex",
		Messages: []adapter.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("an agent turn with no text was reported as a successful answer")
	}
}

var _ = os.Getenv
var _ = filepath.Join

// The CLI also runs in plain-text mode, where the output is not JSON at all and
// the content *is* the answer. Confusing that with "JSON this build does not
// understand" would blank every text-mode run.
func TestPlainTextOutputIsReturnedAsTheAnswer(t *testing.T) {
	a := newTestAdapter(t, `printf 'OK\n'`)
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model:    "gpt-5.3-codex",
		Messages: []adapter.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "OK" {
		t.Fatalf("plain-text output was read as %q, want %q", resp.Content, "OK")
	}
	if resp.Model != "gpt-5.3-codex" {
		t.Fatalf("the response lost the requested model: %q", resp.Model)
	}
}
