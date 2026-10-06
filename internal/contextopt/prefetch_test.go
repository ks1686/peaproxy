package contextopt

import (
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/contextstore"
)

func storeWith(t *testing.T, arts map[string]string) *contextstore.Store {
	t.Helper()
	s := contextstore.New(contextstore.Options{})
	for k, v := range arts {
		if err := s.Put("s1", contextstore.Artifact{Key: k, Body: []byte(v)}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// Speculative retrieval costs tokens whether or not the model reads what it
// fetched. With nothing relevant stored, injecting an empty block would be
// pure overhead, so the request is sent exactly as it arrived.
func TestPrefetchDoesNothingWithAnEmptyStore(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	p := Prefetch{Store: contextstore.New(contextstore.Options{}), Session: "s1"}

	out, injected := p.Apply(body)
	if injected {
		t.Fatalf("injected context with nothing stored: %s", out)
	}
	if string(out) != string(body) {
		t.Fatalf("body changed with nothing stored: %s", out)
	}
}

// Speculative retrieval is the thing that can spend money on context nobody
// reads, so the output is capped by count and by bytes. Both caps are the
// reason this feature is safe to run by default.
func TestPrefetchIsBoundedByCountAndBytes(t *testing.T) {
	arts := map[string]string{}
	for i := 0; i < 20; i++ {
		arts[string(rune('a'+i))+"-key"] = "kubernetes deployment rollout " + strings.Repeat("detail ", 100)
	}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"kubernetes rollout"}]}`)

	byCount := Prefetch{Store: storeWith(t, arts), Session: "s1", MaxPassages: 3, MaxBytes: 1 << 20}
	out, _ := byCount.Apply(body)
	if got := strings.Count(string(out), contextOpen); got > 3 {
		t.Fatalf("%d passages survived a cap of 3", got)
	}

	byBytes := Prefetch{Store: storeWith(t, arts), Session: "s1", MaxPassages: 100, MaxBytes: 512}
	out, _ = byBytes.Apply(body)
	if len(out) > len(body)+1024 {
		t.Fatalf("body grew by %d bytes under a 512-byte cap", len(out)-len(body))
	}
}

// The core promise is fulfilling the same request, not a cheaper version of it.
// Retrieved material must therefore be clearly reference material, never text
// that reads as an instruction the model should follow.
func TestPrefetchedContextIsLabelledAsReferenceNotInstruction(t *testing.T) {
	arts := map[string]string{"k": "the staging cluster runs on port 8443"}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"where is staging"}]}`)

	out, injected := (Prefetch{Store: storeWith(t, arts), Session: "s1"}).Apply(body)
	if !injected {
		t.Fatalf("nothing was retrieved: %s", out)
	}
	text := string(out)
	if !strings.Contains(text, contextOpen) || !strings.Contains(text, contextClose) {
		t.Fatalf("retrieved context is not delimited: %s", out)
	}
	if !strings.Contains(text, "reference") {
		t.Fatalf("retrieved context is not marked as reference material: %s", out)
	}
}

// Whatever was retrieved, the caller's own messages must be intact. PeaProxy
// adds to a request; it does not rewrite one.
func TestPrefetchPreservesTheCallersRequest(t *testing.T) {
	arts := map[string]string{"k": "staging is on port 8443"}
	body := []byte(`{"model":"gpt-4o","temperature":0.2,"messages":[{"role":"user","content":"where is staging"}]}`)

	out, injected := (Prefetch{Store: storeWith(t, arts), Session: "s1"}).Apply(body)
	if !injected {
		t.Fatalf("nothing was retrieved: %s", out)
	}
	for _, must := range []string{`"model":"gpt-4o"`, `"temperature":0.2`, "where is staging"} {
		if !strings.Contains(string(out), must) {
			t.Errorf("caller's request lost %s: %s", must, out)
		}
	}
}

// A passage is quoted, not summarised. Anything else would be PeaProxy editing
// what it retrieved, which is how a fact quietly changes on the way through.
func TestPassagesAreQuotedVerbatim(t *testing.T) {
	original := "staging is on port 8443 (see runbook RB-12)"
	arts := map[string]string{"k": original}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"staging port"}]}`)

	out, injected := (Prefetch{Store: storeWith(t, arts), Session: "s1"}).Apply(body)
	if !injected {
		t.Fatalf("nothing was retrieved: %s", out)
	}
	if !strings.Contains(string(out), original) {
		t.Fatalf("the passage was not quoted verbatim: %s", out)
	}
}

// Retrieval is deterministic. The same request against the same store must
// produce the same request, or a proxy that varies its own input becomes
// impossible to reason about.
func TestPrefetchIsDeterministic(t *testing.T) {
	arts := map[string]string{
		"a": "kubernetes rollout notes",
		"b": "kubernetes ingress config",
		"c": "unrelated billing spreadsheet",
	}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"kubernetes rollout"}]}`)
	p := Prefetch{Store: storeWith(t, arts), Session: "s1"}

	first, _ := p.Apply(body)
	for i := 0; i < 5; i++ {
		again, _ := p.Apply(body)
		if string(again) != string(first) {
			t.Fatalf("prefetch varied between runs:\n1: %s\n2: %s", first, again)
		}
	}
	// The store deliberately holds an irrelevant passage; it must not be
	// retrieved just because the loop happened to run.
	if strings.Contains(string(first), "billing") {
		t.Fatalf("an irrelevant passage was retrieved: %s", first)
	}
}
