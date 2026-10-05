package catalog

import (
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/economics"
)

// A catalog row can carry what OpenRouter publishes: prompt, completion, and
// sometimes cache read and cache write. Routing needs all four to price a
// request, so the row converts into the shared quote type rather than the
// router guessing which field means what.
func TestPriceQuoteCarriesCacheRates(t *testing.T) {
	in, out, cr, cw := 3.0, 15.0, 0.3, 3.75
	p := Price{Input: &in, Output: &out, CacheRead: &cr, CacheWrite: &cw, Currency: "USD", Verified: true}
	q := p.Quote()
	if q.Input == nil || *q.Input != in || q.Output == nil || *q.Output != out {
		t.Fatalf("quote lost the token rates: %#v", q)
	}
	if q.CacheRead == nil || *q.CacheRead != cr {
		t.Fatalf("quote cache read = %v, want %v", q.CacheRead, cr)
	}
	if q.CacheWrite == nil || *q.CacheWrite != cw {
		t.Fatalf("quote cache write = %v, want %v", q.CacheWrite, cw)
	}
	if !q.Verified || q.Currency != "USD" {
		t.Fatalf("quote verification or currency lost: %#v", q)
	}
}

// A provider that publishes no cache pricing leaves those components unknown.
// They must not become zero, which would let a cached turn look free.
func TestPriceQuoteLeavesUnpublishedCacheRatesUnknown(t *testing.T) {
	in, out := 3.0, 15.0
	p := Price{Input: &in, Output: &out, Currency: "USD", Verified: true}
	q := p.Quote()
	if q.CacheRead != nil || q.CacheWrite != nil {
		t.Fatalf("unpublished cache rates became known: %#v", q)
	}
	// The call reads from the cache, and the quote has no rate for it, so the
	// quote cannot cover this call and cannot prove it was free.
	withCache := economics.Usage{Input: 100, Output: 10, CacheRead: 500}
	if q.FreeFor(withCache) {
		t.Fatal("a quote that cannot cover cache use must not prove a call is free")
	}
}

// An unverified row stays unverified after conversion.
func TestPriceQuoteKeepsUnverifiedRowsUnverified(t *testing.T) {
	zero := 0.0
	p := Price{Input: &zero, Output: &zero, Currency: "USD", Verified: false}
	q := p.Quote()
	if q.Verified {
		t.Fatal("conversion verified a row nobody verified")
	}
	if q.FreeFor(zeroUsage()) {
		t.Fatal("an unverified zero quote must not prove a call is free")
	}
}

func TestPriceQuoteCarriesSource(t *testing.T) {
	in, out := 1.0, 2.0
	p := Price{Input: &in, Output: &out, Currency: "USD", Verified: true, Source: "openrouter", ObservedAt: testNow}
	q := p.Quote()
	if q.Source != "openrouter" {
		t.Fatalf("quote source = %q, want openrouter", q.Source)
	}
	if q.ObservedAt.IsZero() {
		t.Fatal("quote lost its observation time")
	}
}
func zeroUsage() economics.Usage { return economics.Usage{} }

var testNow = time.Now()
