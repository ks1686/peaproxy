package economics

import (
	"testing"
	"time"
)

func ptr(v float64) *float64 { return &v }

// A quote prices four things a request can actually consume. A component the
// provider does not publish is nil, which is unknown, not zero.
func TestExpectedCostSumsKnownComponents(t *testing.T) {
	q := Quote{
		Currency: "USD",
		Input:    ptr(3), Output: ptr(15), CacheRead: ptr(0.3), CacheWrite: ptr(3.75),
		Verified: true,
	}
	// One million tokens per unit, so these counts are scaled down.
	cost, ok := q.ExpectedCost(Usage{Input: 1_000_000, Output: 200_000, CacheRead: 2_000_000, CacheWrite: 100_000})
	if !ok {
		t.Fatal("a fully quoted usage must be costable")
	}
	want := 3.0 + 15*0.2 + 0.3*2 + 3.75*0.1
	if diff := cost - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cost = %v, want %v", cost, want)
	}
}

// A component used but not quoted makes the total unknown. Reporting the sum
// of the known parts would understate the bill, and understating a bill is the
// failure mode this package exists to prevent.
func TestExpectedCostUnknownWhenAUsedComponentIsUnquoted(t *testing.T) {
	q := Quote{Currency: "USD", Input: ptr(3), Output: ptr(15), Verified: true}
	if _, ok := q.ExpectedCost(Usage{Input: 1000, Output: 100, CacheRead: 5000}); ok {
		t.Fatal("cache reads were used but unquoted; the total must be unknown")
	}
}

// An unquoted component that was not used cannot make the total unknown: no
// tokens were cached, so there is nothing to price.
func TestExpectedCostUnknownOnlyForUsedComponents(t *testing.T) {
	q := Quote{Currency: "USD", Input: ptr(3), Output: ptr(15), Verified: true}
	if _, ok := q.ExpectedCost(Usage{Input: 1000, Output: 100}); !ok {
		t.Fatal("an unused, unquoted component must not block a known total")
	}
}

func TestExpectedCostRejectsUnverifiedAndForeignCurrency(t *testing.T) {
	q := Quote{Currency: "USD", Input: ptr(3), Output: ptr(15)}
	if _, ok := q.ExpectedCost(Usage{Input: 1000, Output: 100}); ok {
		t.Fatal("an unverified quote must not produce a cost")
	}
	eur := Quote{Currency: "EUR", Input: ptr(3), Output: ptr(15), Verified: true}
	if _, ok := eur.ExpectedCost(Usage{Input: 1000, Output: 100}); ok {
		t.Fatal("a quote in another currency must not be added to a USD ledger")
	}
}

// A free deployment is one with every used component quoted at zero. Anything
// else is not free, and a missing quote is certainly not free.
func TestFreeRequiresEveryUsedComponentQuotedZero(t *testing.T) {
	zero := ptr(0)
	free := Quote{Currency: "USD", Input: zero, Output: zero, CacheRead: zero, CacheWrite: zero, Verified: true}
	if !free.FreeFor(Usage{Input: 10, Output: 10, CacheRead: 10, CacheWrite: 10}) {
		t.Fatal("an all-zero verified quote is free")
	}
	// Both used components are quoted at zero, so this call is provably free.
	partial := Quote{Currency: "USD", Input: zero, Output: zero, Verified: true}
	if !partial.FreeFor(Usage{Input: 10, Output: 10}) {
		t.Fatal("every used component is quoted at zero, so the call is free")
	}
	// Add cache reads and the quote can no longer cover the call, so it can no
	// longer prove the call was free.
	if partial.FreeFor(Usage{Input: 10, Output: 10, CacheRead: 5}) {
		t.Fatal("a quote missing a used component cannot prove the call was free")
	}
	paid := Quote{Currency: "USD", Input: ptr(1), Output: zero, Verified: true}
	if paid.FreeFor(Usage{Input: 5, Output: 10}) {
		t.Fatal("input quoted above zero with input used is not free")
	}
}

// A quote is only as current as its observation. Staleness is reported rather
// than assumed, so routing can refuse to treat an old quote as current.
func TestQuoteStaleness(t *testing.T) {
	q := Quote{Currency: "USD", Input: ptr(3), Output: ptr(15), Verified: true, ObservedAt: time.Now().Add(-48 * time.Hour)}
	if !q.Stale(24 * time.Hour) {
		t.Fatal("a 48h old quote must be stale against a 24h horizon")
	}
	fresh := Quote{Currency: "USD", Input: ptr(3), Output: ptr(15), Verified: true, ObservedAt: time.Now()}
	if fresh.Stale(24 * time.Hour) {
		t.Fatal("a just-observed quote is not stale")
	}
	if (Quote{Currency: "USD", Verified: true}).Stale(24*time.Hour) != true {
		t.Fatal("a quote with no observation time cannot be treated as current")
	}
}
