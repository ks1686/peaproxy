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
type spendWindow func(days int) usage.SpendWindow

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
	return g.ceilingBlocksIn(g.spentInWindow(spendCeilingDays), ceiling, 0)
}

// ceilingBlocksIn is the ceiling decision for one window reading, named apart
// from the read so that a reservation can be taken against the very same
// reading the decision was made on.
//
// pending is spend a request is about to commit and has not been held yet. It
// counts toward the ceiling, because the decision that matters is whether the
// request being sent now would breach it -- not whether some earlier request
// already had.
func (g *Gateway) ceilingBlocksIn(w usage.SpendWindow, ceiling, pending float64) (bool, string) {
	if w.Priced < w.Total {
		// Recorded spend is a floor, not a total. Refusing is the safe
		// direction: the alternative quietly spends money on the accounts
		// whose prices PeaProxy knows least about.
		return true, fmt.Sprintf(
			"refusing to spend: the optimization.spendCeilingUSD ceiling is set to %.2f, but spend in this window cannot be measured because %d of %d calls carried no published price. Set the prices in automaticRoutes, or raise the ceiling once the total is trustworthy",
			ceiling, w.Total-w.Priced, w.Total)
	}
	if total := w.USD + pending; total >= ceiling {
		if pending > 0 {
			return true, fmt.Sprintf(
				"refusing to spend: %.2f USD is already committed over the last %d days, including %.2f USD held by requests in flight, which meets the optimization.spendCeilingUSD ceiling of %.2f",
				total, spendCeilingDays, pending, ceiling)
		}
		return true, fmt.Sprintf(
			"refusing to spend: %.2f USD recorded over the last %d days meets the optimization.spendCeilingUSD ceiling of %.2f",
			w.USD, spendCeilingDays, ceiling)
	}
	return false, ""
}

func (g *Gateway) spentInWindow(days int) usage.SpendWindow {
	if g.spendWindow == nil {
		return usage.SpendWindow{}
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
