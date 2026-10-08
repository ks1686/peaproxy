package gateway

import (
	"github.com/ks1686/peaproxy/internal/economics"
	"github.com/ks1686/peaproxy/internal/usage"
)

// PriceUsage attaches PeaProxy's own cost figure to a call the provider priced
// only in tokens.
//
// A provider that publishes a cost has already said what the call cost, so
// nothing is added: two figures for one call would count the same tokens twice
// against the ceiling. Otherwise the published token counts are priced against
// the deployment's quote, which is the best measurement available for most
// providers -- they publish tokens and no cost, so without this the ledger reads
// every such call as unmeasurable and a ceiling refuses forever.
//
// An estimate is recorded only when the quote can state a complete cost. A call
// that touched a component the provider publishes no rate for stays unmeasured,
// which is the direction that refuses to spend rather than the one that
// understates a bill.
//
// This lives on the Gateway rather than on the server because pricing an event
// needs exactly two things -- the deployment quote and the published counts --
// and both belong to the gateway. Leaving it on the server meant the eval
// harness, which drives a gateway with no server in front of it, had no way to
// price a call and therefore could not test the promise that one must be priced
// before it counts. That promise sat in the gate's exempt list as "structurally
// unbreakable" for a reason that was a layering accident, not a structure.
func (g *Gateway) PriceUsage(e *usage.Event, account, model string) {
	if g == nil || e == nil || e.CostUSD != nil || !e.Costable {
		return
	}
	q := g.QuoteFor(account, model)
	cost, ok := economics.EstimateCost(q, e.PromptTokens, e.CompletionTokens, e.CacheRead, e.CacheWrite, e.CacheReadNested)
	if !ok {
		return
	}
	e.EstimatedUSD = &cost
}
