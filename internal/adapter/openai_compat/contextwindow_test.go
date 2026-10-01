package openai_compat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

// #81: context windows, where the provider actually publishes them and nowhere
// else. OpenRouter sends context_length on all 462 of its models; OpenAI and
// Anthropic send nothing at all through /v1/models. Guessing a value from the
// model name would be worse than showing none, because a harness would believe
// it.

func listFrom(t *testing.T, body string) []catalog.Model {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	a, err := New(adapter.Options{ID: "acct", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return models
}

// Deliberately not gated on the provider name: a self-hosted gateway that
// reports context_length should get the same treatment as OpenRouter.
func TestContextWindowIsReadWhenTheProviderPublishesIt(t *testing.T) {
	models := listFrom(t, `{"data":[
		{"id":"anthropic/claude-sonnet-5.5","context_length":1000000},
		{"id":"openai/gpt-6.1-sol","context_length":1050000}
	]}`)
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	if models[0].ContextWindow != 1000000 {
		t.Errorf("claude-sonnet-5.5 context window: %d, want 1000000", models[0].ContextWindow)
	}
	if models[1].ContextWindow != 1050000 {
		t.Errorf("gpt-6.1-sol context window: %d, want 1050000", models[1].ContextWindow)
	}
}

// The plain OpenAI /v1/models shape carries no context_length. Zero means
// unknown, and unknown has to be distinguishable from a small window.
func TestContextWindowStaysUnknownWhenTheProviderIsSilent(t *testing.T) {
	models := listFrom(t, `{"data":[{"id":"gpt-4.1"},{"id":"o3"}]}`)
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	for _, m := range models {
		if m.ContextWindow != 0 {
			t.Errorf("%s was given an invented context window of %d", m.ID, m.ContextWindow)
		}
	}
}

// Zero, negative, null, a float and a non-number all mean the provider did not
// publish a usable length, and none of them may become a number.
func TestNonPositiveOrUnusableContextWindowIsUnknown(t *testing.T) {
	models := listFrom(t, `{"data":[
		{"id":"zero","context_length":0},
		{"id":"negative","context_length":-5},
		{"id":"null","context_length":null},
		{"id":"float","context_length":128000.5},
		{"id":"words","context_length":"lots"},
		{"id":"absurd","context_length":99999999999}
	]}`)
	for _, m := range models {
		if m.ContextWindow != 0 {
			t.Errorf("%s: context window %d, want 0", m.ID, m.ContextWindow)
		}
	}
}

// One unreadable field must cost that one row its context window, not the
// whole listing. Decoding straight into json.Number fails the entire unmarshal
// instead, which would blank a user's entire catalog over one bad value.
func TestOneBadContextWindowDoesNotCostTheWholeListing(t *testing.T) {
	models := listFrom(t, `{"data":[
		{"id":"good-before","context_length":128000},
		{"id":"broken","context_length":{"nested":true}},
		{"id":"good-after","context_length":256000}
	]}`)
	if len(models) != 3 {
		t.Fatalf("one malformed field cost %d models, want 3", len(models))
	}
	if models[0].ContextWindow != 128000 || models[2].ContextWindow != 256000 {
		t.Errorf("neighbouring rows were damaged: %d, %d", models[0].ContextWindow, models[2].ContextWindow)
	}
	if models[1].ContextWindow != 0 {
		t.Errorf("the broken row invented %d", models[1].ContextWindow)
	}
}

// A quoted integer is not a guess -- it is the same exact number, and providers
// that serialise it that way are common enough to be worth reading.
func TestQuotedIntegerContextWindowIsAccepted(t *testing.T) {
	models := listFrom(t, `{"data":[{"id":"quoted","context_length":"128000"}]}`)
	if models[0].ContextWindow != 128000 {
		t.Errorf("a quoted exact integer was dropped: %d", models[0].ContextWindow)
	}
}

// Unknown stays out of the JSON entirely, so a harness can tell "not published"
// from "128k" without guessing.
func TestUnknownContextWindowIsOmittedFromJSON(t *testing.T) {
	unknown, err := json.Marshal(catalog.Model{ID: "gpt-4.1", Provider: "openai_compat", ContextWindow: 0})
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(unknown), "contextWindow") {
		t.Errorf("an unknown context window is still in the payload: %s", unknown)
	}
	known, err := json.Marshal(catalog.Model{ID: "gpt-4.1", Provider: "openai_compat", ContextWindow: 128000})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(known), `"contextWindow":128000`) {
		t.Errorf("a known context window is missing from the payload: %s", known)
	}
}

// The annotation pass that feeds /admin/catalog must not drop it.
func TestAnnotatedRowsKeepTheContextWindow(t *testing.T) {
	out := catalog.AllAnnotated([]catalog.Model{
		{ID: "a", Provider: "p", AccountID: "acct", ContextWindow: 200000, Exposed: true, Routable: true},
	}, catalog.Query{Filter: catalog.FilterAll})
	if len(out) != 1 {
		t.Fatalf("got %d rows, want 1", len(out))
	}
	if out[0].ContextWindow != 200000 {
		t.Errorf("annotation dropped the context window: %d", out[0].ContextWindow)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
