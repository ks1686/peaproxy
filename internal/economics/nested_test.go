package economics

import "testing"

// OpenAI counts cached tokens inside the prompt total and bills them at the
// cache-read rate instead of the input rate. Charging the prompt total and the
// cached portion as two separate amounts bills those tokens twice, which
// overstates spend -- the safe direction for a refusal, but a false one for a
// ledger that promises to be the best measurement available.
func TestNestedCacheReadsAreNotBilledTwice(t *testing.T) {
	u := NewUsage(1000, 10, 800, 0, true)
	if u.Input != 200 {
		t.Fatalf("input = %d, want 200 uncached tokens", u.Input)
	}
	if u.CacheRead != 800 {
		t.Fatalf("cache read = %d, want 800", u.CacheRead)
	}

	in, out := 3.0, 15.0
	cached := 0.3
	cost, ok := Quote{Input: &in, Output: &out, CacheRead: &cached, Currency: LedgerCurrency, Verified: true}.ExpectedCost(u)
	if !ok {
		t.Fatal("cost could not be stated")
	}
	// 200 uncached input, 800 at the cache rate, 10 output.
	want := 200*in/1e6 + 800*cached/1e6 + 10*out/1e6
	if diff := cost - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost = %v, want %v", cost, want)
	}
}

// Anthropic reports cache reads beside the prompt total, so the prompt count is
// already the uncached one and nothing may be subtracted from it.
func TestSeparateCacheReadsAreNotSubtracted(t *testing.T) {
	u := NewUsage(100, 10, 40, 60, false)
	if u.Input != 100 {
		t.Fatalf("input = %d, want 100", u.Input)
	}
	if u.CacheRead != 40 || u.CacheWrite != 60 {
		t.Fatalf("cache read/write = %d/%d, want 40/60", u.CacheRead, u.CacheWrite)
	}
}

// A provider that counts more cached tokens than prompt tokens is reporting
// something this code cannot reconcile. Subtracting would leave a negative
// uncached count, which would quietly become free input.
func TestNestedCacheReadsCannotExceedThePromptCount(t *testing.T) {
	u := NewUsage(100, 10, 500, 0, true)
	if u.Input < 0 || u.CacheRead != 500 {
		t.Fatalf("input = %d, cache read = %d; want a nonnegative input and the cache count kept", u.Input, u.CacheRead)
	}
	if u.Input != 0 {
		t.Fatalf("input = %d, want 0 when the cache count exceeds the prompt count", u.Input)
	}
}
