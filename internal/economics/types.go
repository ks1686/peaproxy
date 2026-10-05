// Package economics prices a request before it is sent, so PeaProxy can choose
// between deployments on cost without guessing.
//
// The rule throughout: an unpriced component is unknown, never zero. A total
// that cannot be computed is reported as unknown rather than as the sum of the
// parts that happen to be known, because understating a bill is worse than
// declining to state one.
package economics

import "time"

// LedgerCurrency is what PeaProxy totals in. A quote in another currency is not
// converted, because no conversion rate here would be honest.
const LedgerCurrency = "USD"

// unitsPerToken scales a per-million-token rate onto a token count.
const unitsPerToken = 1_000_000

// Quote is a provider's published price for one deployment, at one point in
// time. A nil rate means the provider does not publish that component.
type Quote struct {
	Currency   string
	Input      *float64 // per million input tokens, uncached
	Output     *float64 // per million output tokens
	CacheRead  *float64 // per million tokens read from the provider's cache
	CacheWrite *float64 // per million tokens written to the provider's cache

	// Source names where the numbers came from and ObservedAt says when.
	Source     string
	ObservedAt time.Time

	// Verified means a person or a trusted source confirmed the numbers.
	// An unverified quote never prices a route.
	Verified bool
}

// Usage is what a request would consume. A component the caller does not know
// yet counts as unknown.
type Usage struct {
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int

	// Unknown marks usage counts PeaProxy cannot predict, such as output
	// length before the model has answered. A quote cannot produce a complete
	// total from an unknown estimate, because the missing part is real spend
	// that the total would leave out.
	Unknown bool
}

// ExpectedCost prices a usage estimate against a quote.
//
// ok is false when a complete total cannot be stated: an unverified quote,
// another currency, a component that was used and not quoted, or a usage
// estimate that is not fully known. In that case the returned cost is a partial
// sum and must not be presented as the whole -- ExpectedCostFloor names that
// partial sum for callers that can label it.
func (q Quote) ExpectedCost(u Usage) (cost float64, ok bool) {
	cost, ok = q.sumKnown(u)
	if !ok || u.Unknown {
		return cost, false
	}
	return cost, true
}

// ExpectedCostFloor returns the cost of the parts that are known, for callers
// that can present it as a floor rather than a total. ok is false when even the
// known parts cannot be priced, which happens when the quote itself is unusable.
func (q Quote) ExpectedCostFloor(u Usage) (cost float64, ok bool) {
	return q.sumKnown(u)
}

// sumKnown adds up the priced components and reports whether all of the ones
// this usage actually touches had a published rate.
func (q Quote) sumKnown(u Usage) (cost float64, ok bool) {
	if !q.Verified || q.Currency != LedgerCurrency {
		return 0, false
	}
	known := true
	add := func(tokens int, rate *float64) {
		if tokens == 0 {
			// Nothing consumed, so an unpriced component costs nothing.
			return
		}
		if rate == nil {
			known = false
			return
		}
		cost += float64(tokens) * *rate / unitsPerToken
	}
	add(u.Input, q.Input)
	add(u.Output, q.Output)
	add(u.CacheRead, q.CacheRead)
	add(u.CacheWrite, q.CacheWrite)
	return cost, known
}

// FreeFor reports whether a call on this quote is free.
//
// Every component the call uses must be quoted, and every quoted component must
// be zero. A missing rate is not a zero rate: a provider that publishes no
// cache-write price may still charge for writing one, so a quote that cannot
// cover the call cannot prove the call was free. This is what keeps a
// free-only route from quietly spending money.
// PriceSourceConfig marks a price that came from user configuration rather than
// from a provider's published pricing. A user asserting a price is believed for
// ordinary routing, but a free-only guarantee is a promise about money, and a
// promise needs a measurement behind it.
const PriceSourceConfig = "config"

// Measured reports whether a provider published this price.
//
// An empty source is not measured. It means nobody recorded where the number
// came from, and treating that as trustworthy would let an unlabelled price
// satisfy a free-only guarantee on the strength of nothing.
func (q Quote) Measured() bool {
	return q.Verified && q.Source != "" && q.Source != PriceSourceConfig
}

// GuaranteesFreeTo reports whether a free-only route may rely on this quote.
//
// It is stricter than FreeFor on purpose. FreeFor answers "is this call priced
// at zero", which a user may reasonably state from their own account -- the
// catalog has no opinion about a promotional credit. GuaranteesFreeTo answers
// "can PeaProxy promise the user's money is safe", and a promise made from
// someone's recollection is worth less than one made from a published price.
func (q Quote) GuaranteesFreeTo(u Usage) bool {
	return q.Measured() && q.FreeFor(u)
}

// FreeForTrustedUse reports free-ness for a caller that has been told whether an
// asserted price counts.
//
// trustAsserted is the opt-in and is never implied. A user who knows their
// account is free may ask PeaProxy to believe them, and then it should; but the
// asking has to be visible at the call site, because this is the difference
// between a measured guarantee and a remembered one.
func (q Quote) FreeForTrustedUse(u Usage, trustAsserted bool) bool {
	if !q.FreeFor(u) {
		return false
	}
	return trustAsserted || q.Measured()
}

func (q Quote) FreeFor(u Usage) bool {
	if !q.Verified || q.Currency != LedgerCurrency {
		return false
	}
	zero := func(tokens int, rate *float64) bool {
		if tokens == 0 {
			return true
		}
		return rate != nil && *rate == 0
	}
	return zero(u.Input, q.Input) &&
		zero(u.Output, q.Output) &&
		zero(u.CacheRead, q.CacheRead) &&
		zero(u.CacheWrite, q.CacheWrite)
}

// Stale reports whether an observation is old enough to be doubtful. A quote
// that was never observed is always stale: nobody has seen these numbers.
func (q Quote) Stale(horizon time.Duration) bool {
	if q.ObservedAt.IsZero() {
		return true
	}
	return time.Since(q.ObservedAt) > horizon
}

// Reason explains a decision in one line for the request log and the UI. It
// never carries a provider body or a prompt fragment.
type Reason string

const (
	ReasonNoCandidate     Reason = "no-candidate"
	ReasonCapability      Reason = "capability-mismatch"
	ReasonPrivacy         Reason = "privacy-not-permitted"
	ReasonQuality         Reason = "quality-below-requirement"
	ReasonNotFree         Reason = "not-free"
	ReasonAllowanceSpent  Reason = "allowance-spent"
	ReasonExpiredCredit   Reason = "expired-credit"
	ReasonPriceUnknown    Reason = "price-unknown"
	ReasonCooling         Reason = "account-cooling"
	ReasonLocalOnly       Reason = "local-only-boundary"
	ReasonSpendCeiling    Reason = "spend-ceiling"
	ReasonSelected        Reason = "selected-by-user"
	ReasonSessionPinned   Reason = "session-pinned"
	ReasonCheapest        Reason = "cheapest-eligible"
	ReasonFastestEligible Reason = "fastest-eligible"
)
