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
	dir := fakeCLI(t, `echo ok`)
	a, err := New(adapter.Options{ID: "cursor-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Validate(context.Background()); err != nil {
		t.Fatalf("validation failed while the CLI was present: %v", err)
	}
	// The binary is gone now, but the adapter already exists and will keep
	// trying to exec that exact path. Validation checks the binary the adapter
	// runs, so removing it from PATH alone must not make a dead account look
	// sound -- and conversely, a *different* cursor-agent appearing on PATH
	// must not make a working adapter look broken.
	if err := os.Remove(filepath.Join(dir, Binary)); err != nil {
		t.Fatal(err)
	}
	if err := a.Validate(context.Background()); err == nil {
		t.Fatalf("validation passed after the executable was removed: an account whose every " +
			"call would fail with exec error was reported as sound")
	}
}

// A different cursor-agent appearing on PATH later must not change the verdict
// either. The adapter execs the path it resolved at construction, so that is the
// path whose existence decides whether calls work.
func TestValidationIgnoresALaterPathEntryForADifferentBinary(t *testing.T) {
	dir := fakeCLI(t, `echo ok`)
	a, err := New(adapter.Options{ID: "cursor-test"})
	if err != nil {
		t.Fatal(err)
	}
	// Remove the resolved binary, then put a different one on PATH.
	if err := os.Remove(filepath.Join(dir, Binary)); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, Binary), []byte("#!/bin/sh\necho other\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", other)
	if err := a.Validate(context.Background()); err == nil {
		t.Fatalf("validation passed because some other cursor-agent is on PATH, while every " +
			"call would still fail exec on the path this adapter captured")
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

// Adapters label their own models. A model with no account id shows in the
// catalog with no owner, which quietly removes it from per-account pricing, the
// usage rollup and anything that groups health by account.
func TestListedModelsCarryTheAccountIDAndTier(t *testing.T) {
	fakeCLI(t, `cat <<'SCRIPT'
Available models

auto - Auto (default)
gpt-5.3-codex - Codex 5.3
SCRIPT`)
	a, err := New(adapter.Options{ID: "cursor-sub", Tier: "freemium"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range models {
		if m.AccountID != "cursor-sub" {
			t.Fatalf("model %q has account id %q, want %q", m.ID, m.AccountID, "cursor-sub")
		}
		if string(m.Tier) != "freemium" {
			t.Fatalf("model %q has tier %q, want %q", m.ID, m.Tier, "freemium")
		}
	}
}
