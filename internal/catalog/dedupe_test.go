package catalog

import (
	"encoding/json"
	"testing"
)

// #53: /v1/models listed a model id once per account that could serve it, so
// "mock-1" appeared three times with three accounts. OpenAI clients key on id,
// so every picker showed the duplicates.
func TestToOpenAIListDeduplicatesByID(t *testing.T) {
	in := []Model{
		{ID: "mock-1", AccountID: "a", Pinned: true},
		{ID: "other", AccountID: "a"},
		{ID: "mock-1", AccountID: "b"},
		{ID: "mock-1", AccountID: "c"},
		{ID: "other", AccountID: "b"},
	}
	got := ToOpenAIList(in)
	if len(got.Data) != 2 {
		t.Fatalf("listed %d entries, want 2: %s", len(got.Data), mustJSON(got))
	}
	// First occurrence wins, and the input is already pin-sorted, so the pinned
	// row is the one that survives.
	if got.Data[0].ID != "mock-1" {
		t.Errorf("first entry is %q, want mock-1", got.Data[0].ID)
	}
	if got.Data[1].ID != "other" {
		t.Errorf("second entry is %q, want other", got.Data[1].ID)
	}
	for _, m := range got.Data {
		if m.Object != "model" || m.OwnedBy != "peaproxy" {
			t.Errorf("dedupe changed the shape of a row: %+v", m)
		}
	}
}

// An id that is only present as an <account>:unavailable placeholder is still
// one id.
func TestToOpenAIListDeduplicatesUnavailableRows(t *testing.T) {
	in := []Model{
		{ID: "acct:unavailable", AccountID: "acct"},
		{ID: "acct:unavailable", AccountID: "acct"},
	}
	if got := ToOpenAIList(in); len(got.Data) != 1 {
		t.Fatalf("listed %d entries, want 1: %s", len(got.Data), mustJSON(got))
	}
}

// Empty in, empty out, and still a well-formed list object: a client that
// decodes this must not have to special-case null.
func TestToOpenAIListEmpty(t *testing.T) {
	got := ToOpenAIList(nil)
	if len(got.Data) != 0 {
		t.Fatalf("listed %d entries, want 0", len(got.Data))
	}
	raw := mustJSON(got)
	if raw == "null" || !json.Valid([]byte(raw)) {
		t.Fatalf("empty list is not a valid list object: %s", raw)
	}
}

// The per-account rows are what /admin/catalog shows, so it must not inherit
// the dedupe: a model served by three accounts is three facts there.
func TestAllAnnotatedKeepsOneRowPerAccount(t *testing.T) {
	in := []Model{
		{ID: "mock-1", AccountID: "a"},
		{ID: "mock-1", AccountID: "b"},
	}
	got := AllAnnotated(in, Query{})
	if len(got) != 2 {
		t.Fatalf("AllAnnotated returned %d rows, want 2: %+v", len(got), got)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "marshal error: " + err.Error()
	}
	return string(b)
}
