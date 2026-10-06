package gateway

import (
	"testing"

	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/router"
)

// A user who configures a 0/0 price for a deployment is stating a fact they
// know, and it is honoured. An earlier attempt refused to believe a zero that
// the catalog had not independently observed, which broke this: the account may
// be genuinely free in a way ListModels does not describe, and demanding
// corroboration makes a documented escape hatch useless.
//
// Absence of corroboration must not block. Only contradiction does.
func TestConfiguredFreePriceIsHonoured(t *testing.T) {
	zero := 0.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"acct-b/m": {Input: &zero, Output: &zero, Verified: true},
	}
	row := catalog.Model{ID: "m", AccountID: "acct-b", Tier: catalog.TierPaid}
	gw.models = []catalog.Model{row}

	if !automaticKind(gw, router.RouteFree, row) {
		t.Fatal("a user's own 0/0 price stopped qualifying for pea/free")
	}
}

// What makes the above safe is that an asserted price is distinguishable from a
// measured one. Without this, economics cannot tell a fact the user reported
// from a fact a provider published, and would apply the same rule to both.
func TestConfiguredPriceIsMarkedAsAsserted(t *testing.T) {
	zero := 0.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"acct-b/m": {Input: &zero, Output: &zero, Verified: true},
	}
	row := catalog.Model{ID: "m", AccountID: "acct-b", Tier: catalog.TierPaid}
	gw.models = []catalog.Model{row}

	got := priceForDeployment(gw, "acct-b", "m")
	if got.Source != PriceSourceConfig {
		t.Fatalf("configured price source = %q, want %q", got.Source, PriceSourceConfig)
	}
	if !got.Verified {
		t.Fatal("a user's explicit price should stay verified; it is a statement, not a guess")
	}
}

// A price a provider published keeps its own provenance, so the two never blur.
func TestCatalogPriceKeepsItsPublisher(t *testing.T) {
	zero := 0.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	row := catalog.Model{ID: "m", AccountID: "acct-b", Tier: catalog.TierFree,
		Price: catalog.Price{Input: &zero, Output: &zero, Currency: "USD", Source: "openrouter", Verified: true}}
	gw.models = []catalog.Model{row}

	got := priceForDeployment(gw, "acct-b", "m")
	if got.Source == PriceSourceConfig {
		t.Fatal("a catalog price was reported as user-configured")
	}
	if got.Source != "openrouter" {
		t.Fatalf("catalog price source = %q, want openrouter", got.Source)
	}
}
