package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/router"
)

// optimization.freeOnly says: never spend my money. When it is on, a
// deployment that cannot show a zero price is not a fallback, it is a
// disqualification, on every route -- not only pea/free.
func TestFreeOnlyDisqualifiesAnythingNotProvablyFree(t *testing.T) {
	zero, paid := 0.0, 4.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.Optimization.FreeOnly = boolp(true)
	gw.models = []catalog.Model{
		{ID: "free", AccountID: "acct-a", Tier: catalog.TierFree,
			Price: catalog.Price{Input: &zero, Output: &zero, Currency: "USD", Source: "openrouter", Verified: true}},
		{ID: "costly", AccountID: "acct-b", Tier: catalog.TierPaid,
			Price: catalog.Price{Input: &paid, Output: &paid, Currency: "USD", Source: "openrouter", Verified: true}},
	}

	for _, route := range []string{router.RouteFree, router.RouteEconomy, router.RouteAuto} {
		for _, m := range gw.models {
			allowed := freeOnlyAllows(gw, m)
			if m.ID == "costly" && allowed {
				t.Errorf("route %s admitted a metered deployment while freeOnly is on", route)
			}
			if m.ID == "free" && !allowed {
				t.Errorf("route %s rejected a free deployment while freeOnly is on", route)
			}
		}
	}
}

// The default is off, and that must not change routing for anyone who has not
// asked for it. This is the regression guard for the whole feature.
func TestFreeOnlyDefaultsOffAndChangesNothing(t *testing.T) {
	paid := 4.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	if gw.cfg.FreeOnly() {
		t.Fatal("freeOnly defaulted on")
	}
	row := catalog.Model{ID: "costly", AccountID: "acct-b", Tier: catalog.TierPaid,
		Price: catalog.Price{Input: &paid, Output: &paid, Currency: "USD", Source: "openrouter", Verified: true}}
	gw.models = []catalog.Model{row}
	if !automaticKind(gw, router.RouteEconomy, row) {
		t.Fatal("a metered deployment stopped qualifying for economy with freeOnly off")
	}
}

// A user may assert that their own account is free. Under freeOnly that
// assertion is honoured: they turned the safety on and stated the fact in the
// same breath, and the catalog has no opinion about a promotional credit. The
// provenance is still recorded so any surface that promises safety can tell the
// difference.
func TestFreeOnlyHonoursAnAssertedZeroForAConfiguredDeployment(t *testing.T) {
	zero := 0.0
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.Optimization.FreeOnly = boolp(true)
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"acct-b/m": {Input: &zero, Output: &zero, Verified: true},
	}
	row := catalog.Model{ID: "m", AccountID: "acct-b", Tier: catalog.TierPaid}
	gw.models = []catalog.Model{row}

	if !freeOnlyAllows(gw, row) {
		t.Fatal("freeOnly rejected a deployment the user priced at zero themselves")
	}
	if priceForDeployment(gw, "acct-b", "m").Source != PriceSourceConfig {
		t.Fatal("the asserted price lost its provenance under freeOnly")
	}
}

// An unknown price is not free. "Nobody has looked" must never pass a filter
// whose entire purpose is refusing to guess.
func TestFreeOnlyRejectsAnUnknownPrice(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.Optimization.FreeOnly = boolp(true)
	row := catalog.Model{ID: "m", AccountID: "acct-a", Tier: catalog.TierPaid}
	gw.models = []catalog.Model{row}

	if freeOnlyAllows(gw, row) {
		t.Fatal("an unpriced deployment passed a filter that refuses to guess")
	}
}

// The end-to-end guarantee: with freeOnly on and nothing provably free
// available, the request fails and neither upstream is touched. A filter that
// only rejects candidates would still let a later fallback spend money, and
// "no free route" has to be an error rather than a quiet downgrade.
func TestFreeOnlyFailsTheRequestRatherThanFallingBackToPaid(t *testing.T) {
	aHits, bHits := 0, 0
	paid := 4.0
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "costly"}}})
				return
			}
			aHits++
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "paid"}}}})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "costly"}}})
				return
			}
			bHits++
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "paid"}}}})
		},
	)
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"acct-a/costly": {Input: &paid, Output: &paid, Verified: true},
		"acct-b/costly": {Input: &paid, Output: &paid, Verified: true},
	}
	gw.cfg.Optimization.FreeOnly = boolp(true)

	_, _, err := gw.Chat(context.Background(), []byte(`{"model":"pea/economy","messages":[{"role":"user","content":"hi"}]}`))
	if err == nil {
		t.Fatal("freeOnly allowed a paid request through")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "free") {
		t.Fatalf("error should say why no free route was available, got %q", err)
	}
	if aHits != 0 || bHits != 0 {
		t.Fatalf("money was spent while freeOnly was on: acct-a=%d acct-b=%d", aHits, bHits)
	}
}

// The same setup with freeOnly off must still work, or the feature is a
// regression rather than a setting.
func TestFreeOnlyOffStillSpendsOnTheSameSetup(t *testing.T) {
	paid := 4.0
	hits := 0
	gw := twoAccountGateway(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "costly"}}})
				return
			}
			hits++
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "paid"}}}})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "costly"}}})
				return
			}
			hits++
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "paid"}}}})
		},
	)
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"acct-a/costly": {Input: &paid, Output: &paid, Verified: true},
		"acct-b/costly": {Input: &paid, Output: &paid, Verified: true},
	}

	resp, _, err := gw.Chat(context.Background(), []byte(`{"model":"pea/economy","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("freeOnly is off; the request should have succeeded: %v", err)
	}
	if !strings.Contains(string(resp.Raw), "paid") {
		t.Fatalf("raw = %s", resp.Raw)
	}
	if hits == 0 {
		t.Fatal("no upstream was contacted")
	}
}
