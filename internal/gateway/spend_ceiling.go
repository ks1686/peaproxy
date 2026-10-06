package gateway

import (
	"fmt"

	"github.com/ks1686/peaproxy/internal/economics"
	"github.com/ks1686/peaproxy/internal/usage"
)

// spendWindow returns recorded spend in USD over the last n days, how many
// calls had a known cost, and how many calls ran in total.
//
// The last two are separate because their difference is the whole question. If
// they match, the recorded total is trustworthy. If they do not, some calls
// cost money nobody could measure, and the total is a floor rather than a sum.
type spendWindow func(days int) (usd float64, priced, total int)

// spendCeilingDays is the window a ceiling is measured over. A month is long
// enough to bound a runaway and short enough that a mistake expires on its own.
const spendCeilingDays = 30

// ceilingBlocks reports whether the configured spend ceiling refuses this
// request, and why.
//
// Three rules, in order of how much they matter:
//
//  1. A ceiling of zero means no ceiling. It must never be confused with a
//     budget of zero, or every user who set nothing would be cut off.
//  2. A free deployment is never refused. When the budget is gone, a free
//     request is the one thing the user can still have.
//  3. If recorded spend cannot be trusted -- because calls in the window carried
//     no published price -- the ceiling refuses. Letting the request through
//     would fail open on precisely the accounts most likely to be unpriced,
//     and a ceiling that cannot vouch for its own total is not a ceiling.
func (g *Gateway) ceilingBlocks(u economics.Usage) (bool, string) {
	ceiling := g.cfg.SpendCeiling()
	if ceiling <= 0 {
		return false, ""
	}
	if u.IsFree() {
		return false, ""
	}
	spent, priced, total := g.spentInWindow(spendCeilingDays)

	if priced < total {
		// Recorded spend is a floor, not a total. Refusing is the safe
		// direction: the alternative quietly spends money on the accounts
		// whose prices PeaProxy knows least about.
		return true, fmt.Sprintf(
			"refusing to spend: the optimization.spendCeilingUSD ceiling is set to %.2f, but spend in this window cannot be measured because %d of %d calls carried no published price. Set the prices in automaticRoutes, or raise the ceiling once the total is trustworthy",
			ceiling, total-priced, total)
	}
	if spent >= ceiling {
		return true, fmt.Sprintf(
			"refusing to spend: %.2f USD recorded over the last %d days meets the optimization.spendCeilingUSD ceiling of %.2f",
			spent, spendCeilingDays, ceiling)
	}
	return false, ""
}

func (g *Gateway) spentInWindow(days int) (usd float64, priced, total int) {
	if g.spendWindow == nil {
		return 0, 0, 0
	}
	return g.spendWindow(days)
}

// SetUsage attaches a ledger and rebinds the spend ceiling to it.
//
// Rebinding together is the point. Assigning the store alone would leave the
// ceiling reading a ledger nobody writes to, which reports a permanent zero
// spend and never fires -- a money guard that is quietly inert.
func (g *Gateway) SetUsage(store *usage.Store) {
	g.Usage = store
	if store == nil {
		g.spendWindow = nil
		return
	}
	g.spendWindow = store.SpentInLastDays
}
