package usage

import "testing"

// An OpenAI embedding response publishes prompt_tokens and total_tokens and has
// no completion_tokens field, because the call produces no output tokens.
//
// Requiring both counters priced nothing for money that was spent. The ledger
// then read a priced embedding as unmeasured, and because a ceiling fails closed
// on unmeasured spend, the *next* request was refused -- so wiring embeddings
// into the reservation (v3.0.4) would have turned the first embedding call into
// an outage for anyone with a ceiling set.
func TestAnEmbeddingUsageIsPricedNotTreatedAsIncomplete(t *testing.T) {
	body := []byte(`{"object":"list","model":"text-embedding-3-small",
		"data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],
		"usage":{"prompt_tokens":8,"total_tokens":8}}`)

	var e Event
	ApplyPublishedUsage(&e, body, false)

	if !e.Costable {
		t.Fatal("an embedding call with a published usage block was recorded as unmeasurable")
	}
	if e.PromptTokens != 8 {
		t.Fatalf("prompt tokens = %d, want 8", e.PromptTokens)
	}
	if e.CompletionTokens != 0 {
		t.Fatalf("completion tokens = %d, want 0: the wire says this call has no output tokens", e.CompletionTokens)
	}
}

// The discriminator is the provider's own total agreeing with the prompt total.
// A chat response that omits completion_tokens is a different case and must stay
// unmeasured, because inventing a zero there would price a real call at nothing.
func TestAChatUsageWithoutCompletionTokensStaysUnmeasured(t *testing.T) {
	body := []byte(`{"choices":[],"usage":{"prompt_tokens":1000,"total_tokens":1400}}`)

	var e Event
	ApplyPublishedUsage(&e, body, false)

	if e.Costable {
		t.Fatal("a chat call with no completion counter was priced as if it had produced no output")
	}
	if e.CompletionTokens != 0 || e.PromptTokens != 1000 {
		t.Fatalf("counters = %d/%d, want the published 1000 and nothing invented",
			e.PromptTokens, e.CompletionTokens)
	}
}

// The ordinary chat shape must be untouched by any of this.
func TestAnOrdinaryChatUsageIsStillPriced(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":500}}`)

	var e Event
	ApplyPublishedUsage(&e, body, false)

	if !e.Costable {
		t.Fatal("an ordinary chat call stopped being priceable")
	}
	if e.PromptTokens != 1000 || e.CompletionTokens != 500 {
		t.Fatalf("counters = %d/%d, want 1000/500", e.PromptTokens, e.CompletionTokens)
	}
}
