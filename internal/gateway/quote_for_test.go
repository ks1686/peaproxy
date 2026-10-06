package gateway

import (
	"testing"

	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/router"
	"github.com/ks1686/peaproxy/internal/translate"
)

// The spend ledger can only price a call it knows the deployment of. An exact
// model names it outright, so the quote comes straight from the catalog row or
// the user's configuration.
func TestQuoteForAnExactModel(t *testing.T) {
	in, out := 3.0, 15.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.models = []catalog.Model{{
		ID: "gpt-5", AccountID: "acct-a", Tier: catalog.TierPaid,
		Price: catalog.Price{Input: &in, Output: &out, Currency: "USD", Source: "openrouter", Verified: true},
	}}

	q := gw.QuoteFor("acct-a", "gpt-5")
	if q.Input == nil || *q.Input != in || q.Output == nil || *q.Output != out {
		t.Fatalf("quote = %#v, want the catalog rates", q)
	}
}

// An automatic route names a route, not a deployment. When the account serves
// exactly one model that route may use, the deployment is still unambiguous and
// the call can be priced -- which is the whole point, because most v3 traffic
// goes through these routes.
func TestQuoteForAnAutomaticRouteWithOneCandidate(t *testing.T) {
	in, out := 3.0, 15.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.cfg.AutomaticRoutes.Economy = []string{"m-one"}
	gw.models = []catalog.Model{
		{ID: "m-one", AccountID: "acct-a", Tier: catalog.TierPaid,
			Price: catalog.Price{Input: &in, Output: &out, Currency: "USD", Source: "openrouter", Verified: true}},
		{ID: "m-two", AccountID: "acct-a", Tier: catalog.TierPaid},
	}

	if q := gw.QuoteFor("acct-a", router.RouteEconomy); q.Input == nil || *q.Input != in {
		t.Fatalf("quote = %#v, want the one candidate's rate", q)
	}
}

// Two candidates on one account means the deployment that answered cannot be
// named after the fact. Guessing would price the ledger from a model that may
// not have run, so the honest answer is no quote and the call stays unmeasured.
func TestQuoteForAnAutomaticRouteRefusesToGuess(t *testing.T) {
	in, out := 3.0, 15.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.models = []catalog.Model{
		{ID: "m-one", AccountID: "acct-a", Tier: catalog.TierPaid,
			Price: catalog.Price{Input: &in, Output: &out, Currency: "USD", Source: "openrouter", Verified: true}},
		{ID: "m-two", AccountID: "acct-a", Tier: catalog.TierPaid,
			Price: catalog.Price{Input: &in, Output: &out, Currency: "USD", Source: "openrouter", Verified: true}},
	}

	if q := gw.QuoteFor("acct-a", router.RouteEconomy); q.Input != nil {
		t.Fatalf("quote = %#v, want no quote for an ambiguous deployment", q)
	}
}

// A route's own model list narrows the candidates. A model the route may not use
// cannot have answered, so it must not make the deployment look ambiguous.
func TestQuoteIgnoresModelsTheRouteCannotUse(t *testing.T) {
	in, out := 3.0, 15.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.cfg.AutomaticRoutes.Economy = []string{"m-one"}
	gw.models = []catalog.Model{
		{ID: "m-one", AccountID: "acct-a", Tier: catalog.TierPaid,
			Price: catalog.Price{Input: &in, Output: &out, Currency: "USD", Source: "openrouter", Verified: true}},
		{ID: "other-route", AccountID: "acct-a", Tier: catalog.TierPaid,
			Price: catalog.Price{Input: &in, Output: &out, Currency: "USD", Source: "openrouter", Verified: true}},
	}

	if q := gw.QuoteFor("acct-a", router.RouteEconomy); q.Input == nil {
		t.Fatal("a model the route may not use made the deployment ambiguous")
	}
}

// An unpriced catalog row with a configured price for the same deployment is
// still priceable: the user stated the figure for that account, which is more
// specific than the catalog's account-agnostic view.
func TestQuotePrefersAConfiguredDeploymentPrice(t *testing.T) {
	in, out := 7.0, 21.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"acct-b/m": {Input: &in, Output: &out, Verified: true},
	}

	if q := gw.QuoteFor("acct-b", "m"); q.Input == nil || *q.Input != in {
		t.Fatalf("quote = %#v, want the configured rate", q)
	}
}

// An account nobody can name cannot be priced, even when a bare-model price is
// configured for the model id. That quote says nothing about which deployment
// ran, and applying it to an event recorded against no account would put a
// figure on the ledger that no deployment supports.
func TestQuoteNeedsAnAccount(t *testing.T) {
	in, out := 3.0, 15.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"gpt-5": {Input: &in, Output: &out, Verified: true},
	}
	gw.models = []catalog.Model{{
		ID: "gpt-5", AccountID: "acct-a", Tier: catalog.TierPaid,
		Price: catalog.Price{Input: &in, Output: &out, Currency: "USD", Source: "openrouter", Verified: true},
	}}

	if q := gw.QuoteFor("", "gpt-5"); q.Input != nil || q.Output != nil {
		t.Fatalf("quote = %#v, want an unpriceable quote", q)
	}
}

// A -thinking-N suffix is an opt-in, not a different deployment: routing strips
// it and calls the base id upstream. Pricing the suffixed name missed a quote
// that exists, which left every thinking call unmeasured -- and an unmeasured
// call fails a spend ceiling closed, so the ceiling refused spends PeaProxy was
// able to price.
func TestQuoteForStripsTheThinkingSuffix(t *testing.T) {
	in, out := 3.0, 15.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.models = []catalog.Model{{
		ID: "claude-3", AccountID: "acct-a", Tier: catalog.TierPaid,
		Price: catalog.Price{Input: &in, Output: &out, Currency: "USD", Source: "anthropic", Verified: true},
	}}

	q := gw.QuoteFor("acct-a", "claude-3-thinking-2000")
	if q.Input == nil || *q.Input != in || q.Output == nil || *q.Output != out {
		t.Fatalf("quote = %#v, want the base model's rates; a thinking opt-in is not a different deployment", q)
	}

	// A model whose name legitimately ends that way must not be stripped, and
	// neither must a suffix that is not a budget.
	base, _ := translate.SplitThinkingSuffix("claude-3-thinking-2000")
	if base != "claude-3" {
		t.Fatalf("base = %q", base)
	}
}
