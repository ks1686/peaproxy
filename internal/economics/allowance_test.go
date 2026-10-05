package economics

import (
	"testing"
	"time"
)

// An allowance says what a provider account has left this period and what
// happens when it runs out. The distinction that matters: an account that
// blocks overage cannot quietly spend money, and one that bills overage can,
// even while reporting zero remaining.
func TestAllowanceSpendingStopsOnlyWhenOverageIsBlocked(t *testing.T) {
	none := 0.0
	blocked := Allowance{Kind: AllowanceRecurring, Remaining: &none, Unit: "requests",
		Overage: OverageBlocked, Verified: true, ObservedAt: time.Now(), ResetsAt: time.Now().Add(time.Hour)}
	if blocked.Spendable(time.Now()) {
		t.Fatal("an exhausted allowance that blocks overage must not be spendable")
	}

	billed := blocked
	billed.Overage = OverageBilled
	if !billed.Spendable(time.Now()) {
		t.Fatal("an account with billed overage may still be used; it simply costs money")
	}
}

func TestAllowanceExpiryAndUnknownRemaining(t *testing.T) {
	two := 2.0
	fresh := Allowance{Kind: AllowanceTrial, Remaining: &two, Unit: "requests",
		Overage: OverageBlocked, Verified: true, ObservedAt: time.Now()}
	if !fresh.Spendable(time.Now()) {
		t.Fatal("a verified allowance with remaining capacity is spendable")
	}

	expired := fresh
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	if expired.Spendable(time.Now()) {
		t.Fatal("an expired credit must not be spendable")
	}

	// Remaining is unknown. Spending is allowed, but the allowance cannot be
	// used to promise that a call is free.
	unknown := Allowance{Kind: AllowanceUnknown, Unit: "requests", Overage: OverageUnknown,
		Verified: true, ObservedAt: time.Now()}
	if !unknown.Spendable(time.Now()) {
		t.Fatal("an unknown remaining amount should not block routing on its own")
	}
	if unknown.GuaranteesFree() {
		t.Fatal("an unknown allowance cannot guarantee a call is free")
	}
}

// GuaranteesFree is the predicate a free-only route depends on. It requires the
// provider itself to enforce the ceiling, because a locally tracked balance
// cannot stop spending on an account that is used elsewhere.
func TestAllowanceGuaranteesFreeNeedsProviderEnforcedCeiling(t *testing.T) {
	fifty := 50.0
	now := time.Now()

	strict := Allowance{Kind: AllowanceRecurring, Remaining: &fifty, Unit: "requests",
		Overage: OverageBlocked, Verified: true, ObservedAt: now, ResetsAt: now.Add(24 * time.Hour)}
	if !strict.GuaranteesFree() {
		t.Fatal("a verified allowance with provider-enforced no-charge ceiling guarantees free")
	}

	billed := strict
	billed.Overage = OverageBilled
	if billed.GuaranteesFree() {
		t.Fatal("billed overage cannot guarantee a free call")
	}

	unverified := strict
	unverified.Verified = false
	if unverified.GuaranteesFree() {
		t.Fatal("an unverified allowance cannot guarantee a free call")
	}

	noReset := strict
	noReset.ResetsAt = time.Time{}
	if noReset.GuaranteesFree() {
		t.Fatal("an allowance with no known reset cannot be relied on to still have capacity")
	}
}

// One account's allowance is one balance even when several models draw on it.
func TestAllowanceIsNotMultipliedPerModel(t *testing.T) {
	ten := 10.0
	a := Allowance{Kind: AllowanceRecurring, Remaining: &ten, Unit: "requests",
		Overage: OverageBlocked, Verified: true, ObservedAt: time.Now(), ResetsAt: time.Now().Add(time.Hour)}
	if got := a.AllowedRequests(); got == nil || *got != 10 {
		t.Fatalf("AllowedRequests = %v, want 10 regardless of how many models draw on it", got)
	}
}

func TestAllowanceStaleness(t *testing.T) {
	ten := 10.0
	a := Allowance{Remaining: &ten, Verified: true, ObservedAt: time.Now().Add(-30 * time.Hour)}
	if !a.Stale(24 * time.Hour) {
		t.Fatal("a 30h old observation is stale against a 24h horizon")
	}
	never := Allowance{Remaining: &ten, Verified: true}
	if !never.Stale(24 * time.Hour) {
		t.Fatal("an allowance that was never observed is stale")
	}
}

// A guarantee that ignores staleness is not a guarantee: the balance may have
// been consumed, or the plan changed, since the observation.
func TestGuaranteesFreeRefusesAStaleObservation(t *testing.T) {
	fifty := 50.0
	stale := Allowance{Kind: AllowanceRecurring, Remaining: &fifty, Unit: "requests",
		Overage: OverageBlocked, Verified: true, ResetsAt: time.Now().Add(time.Hour),
		ObservedAt: time.Now().Add(-30 * 24 * time.Hour)}
	if !stale.Stale(24 * time.Hour) {
		t.Fatal("fixture must be stale")
	}
	if stale.GuaranteesFree() {
		t.Fatal("a 30-day-old observation cannot guarantee a call stays free")
	}
	fresh := stale
	fresh.ObservedAt = time.Now()
	if !fresh.GuaranteesFree() {
		t.Fatal("a current observation on the same terms still guarantees free")
	}
}

// A free-only route also needs the reset to be in the future; a reset that has
// already passed means the observation describes a period that is over.
func TestGuaranteesFreeRefusesAPastReset(t *testing.T) {
	fifty := 50.0
	a := Allowance{Kind: AllowanceRecurring, Remaining: &fifty, Unit: "requests",
		Overage: OverageBlocked, Verified: true, ResetsAt: time.Now().Add(-time.Minute),
		ObservedAt: time.Now()}
	if a.GuaranteesFree() {
		t.Fatal("an allowance whose reset has passed cannot guarantee remaining capacity")
	}
}
