package usage

import "testing"

// A usage object that carries only cache counters does not say how many tokens
// the call consumed. Recording that as a known zero-token call would invent a
// number the provider never published, which is the one thing the usage ledger
// is not allowed to do.
//
// https://github.com/ks1686/peaproxy plan T2 review finding F1.
func TestCacheOnlyUsageDoesNotClaimKnownTokens(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens_details":{"cached_tokens":80}}}`)
	e := Event{}
	ApplyPublishedUsage(&e, body, false)

	if e.TokensKnown {
		t.Fatalf("TokensKnown = true for a call whose token counts were never published")
	}
	if e.CacheRead != 80 {
		t.Fatalf("CacheRead = %d, want 80 recorded anyway", e.CacheRead)
	}
	if e.PromptTokens != 0 || e.CompletionTokens != 0 {
		t.Fatalf("token counts = %d/%d, want 0/0", e.PromptTokens, e.CompletionTokens)
	}
}

// Cache counters alongside real token counts are still recorded, and the call
// is still a known-token call.
func TestCacheCountersWithTokensStayKnown(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":4,"cache_read_input_tokens":80}}`)
	e := Event{}
	ApplyPublishedUsage(&e, body, false)
	if !e.TokensKnown || e.PromptTokens != 100 || e.CompletionTokens != 4 || e.CacheRead != 80 {
		t.Fatalf("event = %#v", e)
	}
}
