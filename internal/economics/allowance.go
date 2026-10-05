package economics

import "time"

// AllowanceKind is how an account's free capacity came about.
type AllowanceKind string

const (
	AllowanceRecurring AllowanceKind = "recurring" // resets on a known schedule
	AllowanceTrial     AllowanceKind = "trial"     // promotional, expires
	// AllowancePromotional is a one-off balance that does not reset: a signup
	// grant, a top-up, a limited-time allowance. Once it is gone the account
	// keeps working and the next call is billed.
	AllowancePromotional AllowanceKind = "promotional"
	// AllowanceOneTimeCredit is money handed over once, usually against a
	// card already on file. It has no reset at all, so "exhausted" is not a
	// state that ends.
	AllowanceOneTimeCredit AllowanceKind = "one_time_credit"
	AllowanceSubscription  AllowanceKind = "subscription" // a paid plan's included usage
	AllowanceNone          AllowanceKind = "none"
	// AllowanceUnknown means nobody has observed this account's capacity.
	AllowanceUnknown AllowanceKind = "unknown"
)

// Recurs reports whether this kind of capacity comes back on its own.
func (k AllowanceKind) Recurs() bool {
	return k == AllowanceRecurring || k == AllowanceSubscription
}

// Overage says what the provider does once capacity runs out.
type Overage string

const (
	// OverageBlocked means the provider itself refuses the call. Only this can
	// guarantee that a call stays free.
	OverageBlocked Overage = "blocked"
	// OverageBilled means the provider charges for the excess.
	OverageBilled  Overage = "billed"
	OverageUnknown Overage = "unknown"
)

// Allowance is what one provider account has left, and what happens next.
//
// Remaining is nil when nobody has observed it, which is not the same as zero.
// A nil value never becomes a free guarantee and never becomes a zero in a
// report: it is carried as unknown.
type Allowance struct {
	Kind AllowanceKind

	// Remaining is the amount left this period. Unit names what it counts
	// ("requests", "tokens", "credits"), because a request count cannot be
	// compared with a token count.
	Remaining *float64
	Unit      string

	// ResetsAt is when capacity returns. ExpiresAt is when a trial's capacity
	// stops existing even if it was not used.
	ResetsAt  time.Time
	ExpiresAt time.Time

	// Overage decides what happens past the limit.
	Overage Overage

	// Shared marks an account-wide balance that several models draw on. It is
	// reported so the UI can say so, and it is never multiplied per model.
	Shared bool

	Source     string
	ObservedAt time.Time
	Verified   bool
}

// Spendable reports whether routing may send to this account right now.
//
// A verified allowance with capacity left is spendable. An exhausted one is
// still spendable when the provider bills overage, because the request is
// allowed to cost money -- the caller decides that, not this package. It is
// not spendable when the provider blocks overage, or when a trial has expired.
func (a Allowance) Spendable(now time.Time) bool {
	if !now.Before(a.ExpiresAt) && !a.ExpiresAt.IsZero() {
		return false
	}
	if a.Remaining == nil {
		return true
	}
	if *a.Remaining <= 0 && a.Overage == OverageBlocked {
		return false
	}
	return true
}

// StaleHorizon is how old an observation may be before a free guarantee stops
// being one. A balance read days ago may since have been spent, or the plan
// behind it may have changed.
const StaleHorizon = 24 * time.Hour

// GuaranteesFree reports whether a call on this account cannot cost money.
//
// This is the predicate a free-only route depends on, and it is deliberately
// strict. Every condition that could turn into a charge must be known and
// enforced by the provider: a verified, current observation; a known remaining
// amount; a reset that has not passed; and a ceiling the provider itself
// refuses to cross. A balance PeaProxy tracks locally cannot promise this,
// because the same account may be used by another tool at the same time.
func (a Allowance) GuaranteesFree() bool {
	if !a.Verified || a.Remaining == nil {
		return false
	}
	if a.Stale(StaleHorizon) {
		return false
	}
	if *a.Remaining <= 0 {
		return false
	}
	if a.Overage != OverageBlocked {
		return false
	}
	if a.ExpiresAt.IsZero() && a.ResetsAt.IsZero() {
		return false
	}
	if !a.ResetsAt.IsZero() && !a.ResetsAt.After(time.Now()) {
		return false
	}
	switch a.Kind {
	case AllowanceRecurring:
		// A recurring allowance is only dependable when the next reset is known.
		return !a.ResetsAt.IsZero()
	default:
		// A trial, subscription credit or unknown allowance is dependable while
		// it lasts, but its end is either unknown or not a reprieve.
		return true
	}
}

// Capacity reports what is left and whether that amount is a fact.
//
// The distinction is not cosmetic. A recurring allowance the provider blocks
// past its limit has genuinely reached zero: the next call is refused, not
// billed. A one-time credit or a promotional balance that reads zero is a
// different situation -- the balance is gone, the account still works, and the
// next call costs money. Reporting that as a known zero would let a free-only
// route treat a depleted credit as a harmless exhausted allowance, which is how
// a promotional credit turns into a bill. It is reported as unknown instead.
func (a Allowance) Capacity() (*float64, bool) {
	if a.Remaining == nil {
		return nil, false
	}
	if *a.Remaining <= 0 && !a.Kind.Recurs() && a.Overage != OverageBlocked {
		// Gone, and nothing stops the provider charging for what follows.
		return nil, false
	}
	v := *a.Remaining
	return &v, true
}

// AllowedRequests reports the observed remaining amount, or nil when the unit
// is not a request count or the amount was never observed.
func (a Allowance) AllowedRequests() *float64 {
	if a.Unit != "requests" || a.Remaining == nil {
		return nil
	}
	v := *a.Remaining
	return &v
}

// Stale reports whether the observation is old enough to be doubtful.
func (a Allowance) Stale(horizon time.Duration) bool {
	if a.ObservedAt.IsZero() {
		return true
	}
	return time.Since(a.ObservedAt) > horizon
}
