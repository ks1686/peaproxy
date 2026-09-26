package quota

import (
	"net/http"
	"testing"
)

func TestObserveIgnoresEmptyHeaders(t *testing.T) {
	st := NewStore()
	st.Observe("acct", "openai", http.Header{"Content-Type": []string{"application/json"}})
	if _, ok := st.Get("acct"); ok {
		t.Fatal("empty headers must not create a snapshot")
	}
}

func TestObserveDoesNotOverwriteWithBlank(t *testing.T) {
	st := NewStore()
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests", "9")
	st.Observe("acct", "openai", h)
	st.Observe("acct", "openai", http.Header{})
	got, ok := st.Get("acct")
	if !ok || got.RemainingRequests == nil || *got.RemainingRequests != 9 {
		t.Fatalf("%v %#v", ok, got)
	}
}

func TestObserveStoresHonestZero(t *testing.T) {
	st := NewStore()
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests", "0")
	st.Observe("acct", "groq", h)
	got, ok := st.Get("acct")
	if !ok || got.RemainingRequests == nil || *got.RemainingRequests != 0 {
		t.Fatalf("%v %#v", ok, got)
	}
}

func TestViewsUnknownIsNotReported(t *testing.T) {
	st := NewStore()
	views := st.Views([]Account{{ID: "local", Adapter: "ollama"}})
	if len(views) != 1 {
		t.Fatalf("%#v", views)
	}
	if views[0].Reported() || views[0].Source != SourceNone {
		t.Fatalf("%#v", views[0])
	}
	if views[0].Note == "" {
		t.Fatal("want a not-reported note")
	}
}

func TestViewsMergesStoredHeaders(t *testing.T) {
	st := NewStore()
	h := http.Header{}
	h.Set("x-ratelimit-remaining-tokens", "42")
	st.Observe("oa", "openai", h)
	views := st.Views([]Account{{ID: "oa", Adapter: "openai"}, {ID: "local", Adapter: "ollama"}})
	if len(views) != 2 {
		t.Fatalf("%d", len(views))
	}
	if views[0].RemainingTokens == nil || *views[0].RemainingTokens != 42 {
		t.Fatalf("%#v", views[0])
	}
	if views[1].Reported() {
		t.Fatalf("local invented remaining: %#v", views[1])
	}
}

func TestApplyProbeKeepsHeaderRemaining(t *testing.T) {
	st := NewStore()
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests", "3")
	st.Observe("or", "openrouter", h)
	credits := 74.5
	st.ApplyProbe(Snapshot{
		AccountID:        "or",
		Adapter:          "openrouter",
		Source:           SourceProbe,
		RemainingCredits: &credits,
		Note:             "from OpenRouter GET /key",
	})
	got, _ := st.Get("or")
	if got.RemainingRequests == nil || *got.RemainingRequests != 3 {
		t.Fatalf("probe wiped headers: %#v", got)
	}
	if got.RemainingCredits == nil || *got.RemainingCredits != 74.5 {
		t.Fatalf("%#v", got)
	}
	if got.Source != SourceProbe {
		t.Fatalf("source %s", got.Source)
	}
}
