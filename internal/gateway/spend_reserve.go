package gateway

import (
	"net/http"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/economics"
	"github.com/ks1686/peaproxy/internal/usage"
)

// holdSpend commits part of the spend ceiling to one upstream attempt and
// returns the function that gives it back.
//
// Without this, a ceiling is read once and then enforced by nothing: every
// concurrent request sees the same recorded spend, decides the same request is
// affordable, and goes upstream. The burst that breaches a budget is exactly the
// burst that is least likely to be caught by a check made before it starts.
//
// The hold is taken here, at the one point every attempt passes through, and not
// inside the ceiling check that routing already made. The check filters
// candidates and runs once per candidate model; a reservation is a commitment
// about one request and is taken once, for the deployment actually being called.
//
// Output is not reserved. Nobody can know how long an answer will be before the
// model has written it, and inventing a figure would make the hold a guess
// dressed as a cap. What is held is the request's own input, which is knowable
// now, and what it turns out to be is measured when the call completes.
func (g *Gateway) holdSpend(account, model string, body []byte) (func(), error) {
	noop := func() {}
	ceiling := g.cfg.SpendCeiling()
	store := g.Usage
	if ceiling <= 0 || store == nil || account == "" {
		return noop, nil
	}
	if g.runsOnThisMachine(account, model) {
		// A local deployment costs nothing to run, so holding spend against it
		// would refuse requests that cannot cost money.
		return noop, nil
	}
	estimate := g.inputCostFor(account, model, body)
	if estimate <= 0 {
		// No price, or nothing to price. The ceiling has already been consulted
		// during routing; fabricating a hold here would only invent a number.
		return noop, nil
	}
	w, ok := store.HoldSpend(spendCeilingDays, func(w usage.SpendWindow) bool {
		blocked, _ := g.ceilingBlocksIn(w, ceiling, estimate)
		return !blocked
	}, estimate)
	if !ok {
		if blocked, reason := g.ceilingBlocksIn(w, ceiling, estimate); blocked {
			return noop, ceilingRefusal{reason: reason}
		}
		return noop, nil
	}
	var released bool
	return func() {
		if released {
			return
		}
		released = true
		store.Settle(estimate)
	}, nil
}

// inputCostFor prices the input side of a request before it is sent.
//
// There is no tokenizer here and guessing one would make the figure wrong in the
// direction that matters: an under-estimate holds less than the request will
// cost, which is the same as holding nothing. Every token is at least one byte,
// so the request's byte length is an upper bound on its input tokens and the
// hold can only be too large.
//
// The bound is applied to the whole body because the body is what goes upstream.
// A cache read the provider will discount is charged at the input rate here,
// which over-reserves in exactly the same direction.
func (g *Gateway) inputCostFor(account, model string, body []byte) float64 {
	if len(body) == 0 {
		return 0
	}
	q := priceForDeployment(g, account, model).Quote()
	if q.Input == nil {
		return 0
	}
	cost, ok := q.ExpectedCost(economics.Usage{Input: len(body)})
	if !ok {
		return 0
	}
	return cost
}

// recordDiscardedRound records one retrieval round whose response PeaProxy read
// and then threw away.
//
// A search is a real upstream call and the provider bills it, but the loop kept
// only the last response: everything else was priced by nobody and measured by
// nothing. The ledger therefore read one call where two were paid for, and a
// ceiling computed from it understated the bill by whatever the discarded round
// cost.
//
// Only a discarded round is recorded here. The round returned to the client is
// recorded by the ordinary path, and recording it twice would double-count the
// one call a user can see.
func (g *Gateway) recordDiscardedRound(account, model string, body []byte) {
	if g.Usage == nil || account == "" {
		return
	}
	e := usage.Event{
		Time:      time.Now(),
		AccountID: account,
		Provider:  account,
		Model:     model,
		Protocol:  "openai",
		Path:      "/v1/chat/completions",
		Status:    http.StatusOK,
	}
	usage.ApplyPublishedUsage(&e, body, false)
	if e.CostUSD == nil && e.Costable {
		q := priceForDeployment(g, account, model).Quote()
		if cost, ok := economics.EstimateCost(q, e.PromptTokens, e.CompletionTokens, e.CacheRead, e.CacheWrite, e.CacheReadNested); ok {
			e.EstimatedUSD = &cost
		}
	}
	g.Usage.Add(e)
}

// runsOnThisMachine reports whether a deployment is a local one.
//
// The configured provider tier is consulted as well as the catalog row: a local
// account costs nothing whatever its listing says, and a listing that is stale
// or missing must not turn a free machine into a paid one.
func (g *Gateway) runsOnThisMachine(account, model string) bool {
	if inst := g.instanceFor(account); strings.EqualFold(inst.Provider.Tier, "local") {
		return true
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, m := range g.models {
		if m.ID != model || m.AccountID != account {
			continue
		}
		return m.Tier == catalog.TierLocal
	}
	return false
}

// ceilingRefusal marks a refusal that came from the money guard rather than from
// a provider, so it is reported as a refusal and not retried against another
// account. Spending the same request somewhere else is not what was asked for.
type ceilingRefusal struct{ reason string }

func (e ceilingRefusal) Error() string { return e.reason }

// RoundLimitError reports that PeaProxy stopped answering its own retrieval
// tool because the model would not stop asking for it.
//
// It is a distinct type so the request can fail cleanly. Returning the round
// instead would hand the client a tool call for a tool it never declared,
// which is a bug in the proxy wearing the costume of an answer.
type RoundLimitError struct{ reason string }

func (e RoundLimitError) Error() string { return e.reason }
