package usage

import (
	"context"
	"math"
	"testing"
)

func allow(SpendWindow) bool { return true }

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// A request PeaProxy refused took no hold. Recording it must not release the
// hold of a different request that is still in flight: that drops committed
// spend out of the ceiling and lets a third request through on top of it.
func TestARefusedRequestDoesNotReleaseAnotherRequestsHold(t *testing.T) {
	s := Open("")
	inFlight := HoldsFrom(WithHolds(context.Background()))
	if _, ok := s.HoldSpendOwned(inFlight, 7, allow, 0.60); !ok {
		t.Fatal("hold refused")
	}
	refused := HoldsFrom(WithHolds(context.Background()))
	s.Add(Event{AccountID: "a", Model: "m", NotDispatched: true, Holds: refused.Take(), HoldsTracked: true})
	if got := s.Reserved(); !near(got, 0.60) {
		t.Fatalf("reserved = %v after a refused request, want the in-flight 0.60 still held", got)
	}
}

// Settling a small, newer request releases its own hold, not an older,
// larger one that is still running.
func TestAnEventReleasesItsOwnHold(t *testing.T) {
	s := Open("")
	big := HoldsFrom(WithHolds(context.Background()))
	small := HoldsFrom(WithHolds(context.Background()))
	if _, ok := s.HoldSpendOwned(big, 7, allow, 0.60); !ok {
		t.Fatal("hold refused")
	}
	if _, ok := s.HoldSpendOwned(small, 7, allow, 0.10); !ok {
		t.Fatal("hold refused")
	}
	cost := 0.09
	s.Add(Event{AccountID: "a", Model: "m", TokensKnown: true, Costable: true, CostUSD: &cost, Holds: small.Take(), HoldsTracked: true})
	if got := s.Reserved(); !near(got, 0.60) {
		t.Fatalf("reserved = %v, want the older 0.60 hold still in force", got)
	}
}

// A request that failed over took one hold per attempt and records one event;
// that event releases every hold the request took.
func TestAnEventReleasesEveryHoldItsRequestTook(t *testing.T) {
	s := Open("")
	req := HoldsFrom(WithHolds(context.Background()))
	for i := 0; i < 3; i++ {
		if _, ok := s.HoldSpendOwned(req, 7, allow, 0.20); !ok {
			t.Fatal("hold refused")
		}
	}
	s.Add(Event{AccountID: "a", Model: "m", Holds: req.Take(), HoldsTracked: true})
	if got := s.Reserved(); got != 0 {
		t.Fatalf("reserved = %v, want 0 once the request's event is recorded", got)
	}
}

// An event recorded outside a tracked request cannot claim a hold a tracked
// request owns.
func TestAnUntrackedEventLeavesOwnedHoldsAlone(t *testing.T) {
	s := Open("")
	owned := HoldsFrom(WithHolds(context.Background()))
	if _, ok := s.HoldSpendOwned(owned, 7, allow, 0.40); !ok {
		t.Fatal("hold refused")
	}
	s.Add(Event{AccountID: "a", Model: "m"})
	if got := s.Reserved(); !near(got, 0.40) {
		t.Fatalf("reserved = %v, want the owned 0.40 still held", got)
	}
}
