package usage

import (
	"testing"
	"time"
)

// The point of pricing published token counts: a provider that publishes tokens
// but no cost used to leave every call unpriced, so a ceiling set against such
// a provider could never be satisfied and refused every paid request. An
// estimate derived from the deployment's quote is the best measurement
// available, and it is only recorded when it can be stated completely.
func TestEstimatedSpendCountsAsPriced(t *testing.T) {
	s := Open("")
	s.Add(Event{Time: time.Now(), TokensKnown: true, PromptTokens: 1_000_000, CompletionTokens: 0,
		Costable: true, EstimatedUSD: f64(3)})
	s.Add(Event{Time: time.Now(), TokensKnown: true, Costable: true, EstimatedUSD: f64(0)}) // a cache hit

	w := s.SpentInLastDays(30)
	if w.Priced != 2 || w.Total != 2 {
		t.Fatalf("priced=%d total=%d, want 2 of 2; an estimated cost is still a measurement", w.Priced, w.Total)
	}
	if w.USD != 3 {
		t.Fatalf("usd = %v, want 3", w.USD)
	}
	if w.EstimatedUSD != 3 {
		t.Fatalf("estimated = %v, want 3; the estimate has to stay separable from the provider's own figure", w.EstimatedUSD)
	}
}

// A provider's own cost figure is authoritative and is never double-counted by
// an estimate computed from the same call's tokens.
func TestProviderCostIsNotAlsoEstimated(t *testing.T) {
	s := Open("")
	s.Add(Event{Time: time.Now(), TokensKnown: true, Costable: true,
		PromptTokens: 1_000_000, CostUSD: f64(2), EstimatedUSD: f64(3)})

	w := s.SpentInLastDays(30)
	if w.USD != 2 {
		t.Fatalf("usd = %v, want 2", w.USD)
	}
	if w.EstimatedUSD != 0 {
		t.Fatalf("estimated = %v, want 0 when the provider published its own cost", w.EstimatedUSD)
	}
	if w.Priced != 1 || w.Total != 1 {
		t.Fatalf("priced=%d total=%d, want 1 of 1", w.Priced, w.Total)
	}
}

// Tokens published on only one side cannot price a call. The missing half is
// real spend, so the call stays unmeasured rather than being recorded at half
// its cost.
func TestPartialUsageCannotBeEstimated(t *testing.T) {
	e := Event{}
	// A body that published the prompt count but never the completion count.
	ApplyPublishedUsage(&e, []byte(`{"usage":{"prompt_tokens":500}}`), false)
	if !e.TokensKnown {
		t.Fatal("published tokens were not recorded as known")
	}
	if e.Costable {
		t.Fatal("half a call's usage was marked priceable")
	}

	s := Open("")
	s.Add(e)
	w := s.SpentInLastDays(30)
	if w.Priced != 0 || w.Total != 1 {
		t.Fatalf("priced=%d total=%d, want 0 of 1", w.Priced, w.Total)
	}
}

// The provider's usage shape decides whether cache reads sit inside the prompt
// count. OpenAI reports cached tokens as a detail of the prompt total; charging
// both the prompt total and the cached portion bills the cached tokens twice.
func TestNestedCacheReadsAreMarked(t *testing.T) {
	for name, body := range map[string][]byte{
		"openai chat":      []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":800}}}`),
		"openai responses": []byte(`{"response":{"usage":{"input_tokens":1000,"output_tokens":10,"input_tokens_details":{"cached_tokens":800}}}}`),
	} {
		e := Event{}
		ApplyPublishedUsage(&e, body, false)
		if !e.CacheReadNested {
			t.Errorf("%s: cached tokens inside the prompt count were not marked nested", name)
		}
		if e.CacheRead != 800 {
			t.Errorf("%s: cache read = %d, want 800", name, e.CacheRead)
		}
	}
}

// Anthropic counts cache reads beside the prompt total, so they are not nested
// and pricing must not subtract them.
func TestAnthropicCacheReadsAreNotNested(t *testing.T) {
	e := Event{}
	ApplyPublishedUsage(&e, []byte(`{"usage":{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":60,"output_tokens":10}}`), false)
	if e.CacheReadNested {
		t.Fatal("Anthropic cache reads were marked nested")
	}
	if e.CacheRead != 40 || e.CacheWrite != 60 {
		t.Fatalf("cache read/write = %d/%d, want 40/60", e.CacheRead, e.CacheWrite)
	}
}
