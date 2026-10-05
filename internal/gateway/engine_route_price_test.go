package gateway

import (
	"testing"

	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/router"
)

// A price belongs to a deployment, not to a model name. PeaProxy looked prices
// up by model id alone, so two accounts exposing the same model id shared one
// quote: a free deployment made a paid one look free, and pea/free would route
// to the paid account.
//
// https://github.com/ks1686/peaproxy plan T3 / defect D5.
func TestAutomaticKindUsesTheDeploymentOwnPrice(t *testing.T) {
	zero, paid := 0.0, 4.0
	free := catalog.Model{ID: "m", AccountID: "acct-a", Price: catalog.Price{Input: &zero, Output: &zero, Verified: true}}
	costly := catalog.Model{ID: "m", AccountID: "acct-b", Price: catalog.Price{Input: &paid, Output: &paid, Verified: true}}

	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.models = []catalog.Model{free, costly}

	if !automaticKind(gw, router.RouteFree, free) {
		t.Fatal("free deployment does not qualify for pea/free")
	}
	if automaticKind(gw, router.RouteFree, costly) {
		t.Fatal("paid deployment qualified for pea/free using another account's zero price")
	}
}

// An explicit quote is keyed the same way: a user price for one account must
// not silently price a different account's deployment of the same model.
func TestExplicitQuotesAreScopedToAnAccount(t *testing.T) {
	zero, paid := 0.0, 4.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"acct-a/m": {Input: &zero, Output: &zero, Verified: true},
	}
	gw.models = []catalog.Model{
		{ID: "m", AccountID: "acct-b", Price: catalog.Price{Input: &paid, Output: &paid, Verified: true}},
	}

	if got := gw.priceForDeployment("acct-a", "m"); !got.Free() {
		t.Fatalf("acct-a/m price = %#v, want verified zero", got)
	}
	if got := gw.priceForDeployment("acct-b", "m"); got.Free() {
		t.Fatalf("acct-b/m price = %#v, want not free", got)
	}
}

// A deployment with no verified price anywhere is unknown, never free.
func TestUnpricedDeploymentIsNotFree(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.models = []catalog.Model{{ID: "m", AccountID: "acct-a"}}
	if automaticKind(gw, router.RouteFree, gw.models[0]) {
		t.Fatal("a deployment with no verified price qualified for pea/free")
	}
}
