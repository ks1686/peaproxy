package economics

import (
	"testing"
	"time"
)

// Not every "zero left" means the same thing, and the difference decides
// whether PeaProxy can promise a call is free.
//
// An allowance with provider-blocked overage that reads zero is genuinely
// exhausted and safe: the provider will refuse, not bill.
//
// A one-time promotional credit that reads zero is a different fact. The
// balance is gone, the account still works, and the next call is billed. That
// is not "harmless zero", it is "we no longer know what this will cost", and
// the honest report is unknown. Reporting zero would let a free-only route
// treat a depleted credit as a harmless exhausted allowance.
func TestExhaustedOneTimeCreditIsUnknownNotZero(t *testing.T) {
	zero := 0.0
	a := Allowance{
		Kind:       AllowanceOneTimeCredit,
		Remaining:  &zero,
		Unit:       "credits",
		ObservedAt: time.Now(),
		Verified:   true,
		Overage:    OverageBilled,
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	}

	if v, known := a.Capacity(); known {
		t.Fatalf("a depleted one-time credit reported %v as a known capacity", *v)
	}
	if a.GuaranteesFree() {
		t.Fatal("a depleted one-time credit promised a free call")
	}
}

// The same exhaustion on a recurring, provider-blocked allowance is a true zero.
// There the provider refuses the call rather than billing it, so reporting zero
// is accurate and loses nothing.
func TestExhaustedBlockedRecurringAllowanceIsAKnownZero(t *testing.T) {
	zero := 0.0
	a := Allowance{
		Kind:       AllowanceRecurring,
		Remaining:  &zero,
		Unit:       "requests",
		ResetsAt:   time.Now().Add(12 * time.Hour),
		ObservedAt: time.Now(),
		Verified:   true,
		Overage:    OverageBlocked,
	}

	v, known := a.Capacity()
	if !known || v == nil {
		t.Fatal("a blocked recurring allowance reported unknown instead of an exhausted zero")
	}
	if *v != 0 {
		t.Fatalf("capacity = %v, want 0", *v)
	}
	if a.GuaranteesFree() {
		t.Fatal("an exhausted allowance promised a free call")
	}
	// Spendable means "will this call work", not "is it free". The provider
	// refuses past the limit, so routing there would fail on purpose.
	if a.Spendable(time.Now()) {
		t.Fatal("a blocked, exhausted allowance must not be routed to; the provider will refuse")
	}
}

// A one-time credit that still has balance can promise free, because the
// provider will refuse rather than bill once it runs out mid-call.
func TestOneTimeCreditWithBalanceStillGuaranteesFree(t *testing.T) {
	five := 5.0
	a := Allowance{
		Kind:       AllowanceOneTimeCredit,
		Remaining:  &five,
		Unit:       "credits",
		ObservedAt: time.Now(),
		Verified:   true,
		Overage:    OverageBlocked,
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	}

	if !a.GuaranteesFree() {
		t.Fatal("a funded, provider-enforced one-time credit could not promise a free call")
	}
}

// A promotional allowance that has run out is the same unknown case: the
// balance is gone and the account keeps working.
func TestExhaustedPromotionalAllowanceIsUnknownNotZero(t *testing.T) {
	zero := 0.0
	a := Allowance{
		Kind:       AllowancePromotional,
		Remaining:  &zero,
		Unit:       "requests",
		ObservedAt: time.Now(),
		Verified:   true,
		Overage:    OverageBilled,
		ExpiresAt:  time.Now().Add(7 * 24 * time.Hour),
	}

	if _, known := a.Capacity(); known {
		t.Fatal("a spent promotional allowance reported a known capacity")
	}
	if a.GuaranteesFree() {
		t.Fatal("a spent promotional allowance promised a free call")
	}
}

// Recurring capacity is never downgraded to unknown: it comes back on a known
// schedule, which is the whole point of the kind.
func TestRecurringAllowanceCapacityIsAlwaysKnown(t *testing.T) {
	half := 50.0
	a := Allowance{
		Kind:       AllowanceRecurring,
		Remaining:  &half,
		Unit:       "requests",
		ResetsAt:   time.Now().Add(6 * time.Hour),
		ObservedAt: time.Now(),
		Verified:   true,
		Overage:    OverageBlocked,
	}
	if _, known := a.Capacity(); !known {
		t.Fatal("a recurring allowance reported unknown capacity")
	}
}

// An allowance nobody has measured stays unknown regardless of kind. A kind is
// a claim; an observation is what makes it usable.
func TestUnobservedAllowanceIsUnknownWhateverTheKind(t *testing.T) {
	for _, k := range []AllowanceKind{
		AllowanceRecurring, AllowancePromotional,
		AllowanceOneTimeCredit, AllowanceTrial,
	} {
		a := Allowance{Kind: k, Overage: OverageBlocked}
		if _, known := a.Capacity(); known {
			t.Errorf("%s: an unobserved allowance reported known capacity", k)
		}
	}
}

// A price the user typed and a price a provider published are both "verified",
// because both are statements someone stands behind. But only one is a
// measurement, and a free-only guarantee is a promise about money, so the
// stricter question is separate from the ordinary one.
func TestFreeOnlyRefusesAnAssertedPrice(t *testing.T) {
	zero := 0.0
	flat := Usage{Input: 100, Output: 20}

	measured := Quote{Input: &zero, Output: &zero, Currency: LedgerCurrency,
		Source: "openrouter", Verified: true}
	asserted := Quote{Input: &zero, Output: &zero, Currency: LedgerCurrency,
		Source: PriceSourceConfig, Verified: true}

	// FreeFor is the ordinary question and does not care where the price came
	// from: the user knows their account, and routing must keep working.
	if !measured.FreeFor(flat) {
		t.Fatal("a published zero price should still read as free")
	}
	if !asserted.FreeFor(flat) {
		t.Fatal("a user-typed zero stopped reading as free for ordinary routing")
	}
	// GuaranteesFreeTo is the money promise, and that is where provenance bites.
	if asserted.GuaranteesFreeTo(flat) {
		t.Fatal("a user-typed zero satisfied a free-only guarantee on its own")
	}
	// Refusing outright would be hostile in the same way the routing change was:
	// the user may know their account is free in a way no catalog shows. So the
	// escape exists, and it has to be asked for at the call site.
	if asserted.FreeForTrustedUse(flat, true) != true {
		t.Fatal("a user-typed zero should be admissible when the caller trusts asserted prices")
	}
	if asserted.FreeForTrustedUse(flat, false) {
		t.Fatal("a user-typed zero was trusted without the opt-in")
	}
	if !measured.GuaranteesFreeTo(flat) {
		t.Fatal("a published zero should be admissible for a free-only guarantee")
	}
	if !measured.FreeForTrustedUse(flat, false) {
		t.Fatal("a published zero should not need the opt-in")
	}
}

// An unknown price source is not silently trusted as measured either.
func TestFreeOnlyRefusesAnUnknownPriceSource(t *testing.T) {
	zero := 0.0
	q := Quote{Input: &zero, Output: &zero, Currency: LedgerCurrency, Verified: true}
	if q.GuaranteesFreeTo(Usage{Input: 10}) {
		t.Fatal("a zero price with no publisher satisfied a free-only guarantee")
	}
}

// GuaranteesFree is the strict predicate, and it had a permissive default: any
// kind it did not name fell through to "dependable while it lasts". That let an
// allowance of kind unknown, or even kind none, promise a call was free as long
// as the numbers around it looked right.
//
// A money guarantee needs a known kind of capacity behind it. "We did not
// record what this is" is not capacity, and "there is none" is certainly not.
func TestGuaranteesFreeRefusesUnknownAndAbsentKinds(t *testing.T) {
	five := 5.0
	base := func() Allowance {
		return Allowance{
			Remaining:  &five,
			Unit:       "requests",
			ObservedAt: time.Now(),
			Verified:   true,
			Overage:    OverageBlocked,
			ExpiresAt:  time.Now().Add(24 * time.Hour),
		}
	}
	for _, k := range []AllowanceKind{AllowanceUnknown, AllowanceNone, AllowanceKind("")} {
		a := base()
		a.Kind = k
		if a.GuaranteesFree() {
			t.Errorf("kind %q promised a free call from numbers it did not understand", k)
		}
	}

	// The kinds that do describe real capacity keep working.
	for _, k := range []AllowanceKind{
		AllowanceRecurring, AllowanceTrial, AllowancePromotional,
		AllowanceOneTimeCredit, AllowanceSubscription,
	} {
		a := base()
		a.Kind = k
		if k == AllowanceRecurring {
			a.ResetsAt = time.Now().Add(time.Hour)
		}
		if !a.GuaranteesFree() {
			t.Errorf("kind %q is real capacity and should be able to promise free", k)
		}
	}
}
