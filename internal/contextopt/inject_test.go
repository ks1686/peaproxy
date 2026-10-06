package contextopt

import (
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/requestmeta"
)

// PeaProxy's retrieval tool is executed by PeaProxy, not by the client. If the
// client cannot run tools, a model that calls it produces a tool call the
// client will never answer: the turn stalls or the tool block is shown to a
// user as a broken call. That is worse than not offering retrieval at all.
func TestNoInjectionWhenTheClientCannotRunTools(t *testing.T) {
	for _, wire := range []requestmeta.Wire{
		requestmeta.WireChat, requestmeta.WireMessages, requestmeta.WireResponses,
	} {
		body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
		out, injected := Inject(body, Plan{
			ClientTools:   false,
			ProviderTools: true,
			Enabled:       true,
		})
		if injected {
			t.Errorf("%s: injected a tool the client cannot execute: %s", wire, out)
		}
		if string(out) != string(body) {
			t.Errorf("%s: body was modified without injection: %s", wire, out)
		}
	}
}

// A provider that cannot do tools is just as fatal: the model could never call
// the tool even if the client could have run the answer.
func TestNoInjectionWhenTheProviderCannotDoTools(t *testing.T) {
	body := []byte(`{"model":"m","messages":[]}`)
	out, injected := Inject(body, Plan{ClientTools: true, ProviderTools: false, Enabled: true})
	if injected {
		t.Fatalf("injected a tool for a provider without tool support: %s", out)
	}
	if string(out) != string(body) {
		t.Fatalf("body modified without injection: %s", out)
	}
}

// The feature is opt-in. Nobody gets proxy-owned tools by upgrading.
func TestNoInjectionUnlessEnabled(t *testing.T) {
	body := []byte(`{"model":"m","messages":[]}`)
	if _, injected := Inject(body, Plan{ClientTools: true, ProviderTools: true, Enabled: false}); injected {
		t.Fatal("injected without being enabled")
	}
}

// A client that already declared tools is the case that works.
func TestInjectionWhenBothSidesSupportTools(t *testing.T) {
	body := []byte(`{"model":"m","messages":[],"tools":[]}`)
	out, injected := Inject(body, Plan{ClientTools: true, ProviderTools: true, Enabled: true})
	if !injected {
		t.Fatalf("did not inject where tools work: %s", out)
	}
	if !strings.Contains(string(out), ToolName) {
		t.Fatalf("tool is missing from the body: %s", out)
	}
}

// The caller's tools must survive untouched. PeaProxy adds its own; it never
// rewrites or drops what the client sent.
func TestCallerToolsArePreserved(t *testing.T) {
	body := []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"caller_tool","parameters":{"type":"object"}}}]}`)
	out, injected := Inject(body, Plan{ClientTools: true, ProviderTools: true, Enabled: true})
	if !injected {
		t.Fatalf("did not inject: %s", out)
	}
	if !strings.Contains(string(out), "caller_tool") {
		t.Fatalf("the caller's tool was lost: %s", out)
	}
	if !strings.Contains(string(out), ToolName) {
		t.Fatalf("the proxy tool is missing: %s", out)
	}
}

// PeaProxy's own tool must not be injected twice on a retry or a re-entrant
// call, which would produce two identically-named tools.
func TestInjectionIsNotRepeated(t *testing.T) {
	body := []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"` + ToolName + `"}}]}`)
	out, injected := Inject(body, Plan{ClientTools: true, ProviderTools: true, Enabled: true})
	if injected {
		t.Fatalf("injected a duplicate of its own tool: %s", out)
	}
	if string(out) != string(body) {
		t.Fatalf("body modified on a no-op injection: %s", out)
	}
}

// A body PeaProxy cannot parse is passed through untouched rather than dropped
// or rewritten into something it invented.
func TestUnparseableBodyIsPassedThrough(t *testing.T) {
	for _, body := range []string{`{"model":`, `not json`, ``, `[]`} {
		out, injected := Inject([]byte(body), Plan{ClientTools: true, ProviderTools: true, Enabled: true})
		if injected {
			t.Errorf("%q was modified: %s", body, out)
		}
		if string(out) != body {
			t.Errorf("%q changed to %q", body, out)
		}
	}
}

// A model that keeps calling the retrieval tool must not turn one request into
// an unbounded loop against a metered provider. The budget is what stops it.
func TestRoundBudgetStopsARepeatingModel(t *testing.T) {
	b := NewBudget()
	for i := 0; i < MaxRounds; i++ {
		if !b.Take() {
			t.Fatalf("round %d was refused inside the budget of %d", i+1, MaxRounds)
		}
	}
	if b.Take() {
		t.Fatal("a model exceeding the round budget was allowed another round")
	}
	if b.Exhausted() != true {
		t.Fatal("budget did not report itself exhausted")
	}
}

// A budget must not hand out rounds it does not have, and asking must not
// change the answer: two callers see the same state.
func TestBudgetIsNotAffectedByReading(t *testing.T) {
	b := NewBudget()
	if b.Exhausted() {
		t.Fatal("a fresh budget reported itself exhausted")
	}
	b.Take()
	if b.Exhausted() {
		t.Fatal("a partially used budget reported itself exhausted")
	}
	if b.Remaining() != MaxRounds-1 {
		t.Fatalf("remaining = %d, want %d", b.Remaining(), MaxRounds-1)
	}
}

// When the budget runs out, the turn has to end honestly. PeaProxy must not
// fabricate a terminal event to cover for the truncation, and must report that
// it stopped rather than presenting a partial answer as a complete one.
func TestExhaustionIsReportedNotHidden(t *testing.T) {
	b := NewBudget()
	for i := 0; i < MaxRounds; i++ {
		b.Take()
	}
	stop := b.Stop()
	if stop == "" {
		t.Fatal("exhaustion produced no explanation")
	}
	if !strings.Contains(stop, "pea_search") {
		t.Fatalf("the explanation does not name the tool: %q", stop)
	}
}
