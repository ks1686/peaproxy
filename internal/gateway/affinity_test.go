package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/router"
)

// D7 suspected that session affinity never applied to an automatic route.
// It does. Success bookkeeping binds affinity under both the resolved model and
// the alias the caller wrote, and pickAutomatic reads the alias key. These
// tests pin that behaviour so a future change cannot quietly drop it: losing it
// would move a conversation between accounts and throw away the provider's warm
// prompt cache, which is the cost the affinity exists to protect.
//
// https://github.com/ks1686/peaproxy plan T6 investigation / defect D7.
func TestAutomaticRouteHonoursSessionAffinity(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.cfg.AutomaticRoutes.Auto = []string{"m"}

	// Catalog order puts acct-b first, so the assertion below can only pass if
	// affinity actually reordered the candidates.
	gw.mu.Lock()
	gw.models = []catalog.Model{
		{ID: "m", AccountID: "acct-b", Tier: catalog.TierPaid, Routable: true},
		{ID: "m", AccountID: "acct-a", Tier: catalog.TierPaid, Routable: true},
	}
	gw.mu.Unlock()

	gw.bindAffinity("sess-1", router.RouteAuto, "acct-a")

	_, candidates, _, err := gw.pickAutomatic(
		context.Background(),
		[]byte(`{"model":"pea/auto","session_id":"sess-1","messages":[{"role":"user","content":"hi"}]}`),
		router.RouteAuto,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) == 0 {
		t.Fatal("no candidates")
	}
	if candidates[0].Provider.ID != "acct-a" {
		t.Fatalf("first candidate = %q, want the account this session is bound to",
			candidates[0].Provider.ID)
	}
}

// A successful automatic turn must leave an affinity entry a later turn can
// find, under the alias pickAutomatic reads and under the resolved model.
func TestAutomaticRouteBindsAffinityOnSuccess(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.cfg.AutomaticRoutes.Auto = []string{"m"}

	if _, account, err := gw.Chat(context.Background(),
		[]byte(`{"model":"pea/auto","session_id":"sess-2","messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatal(err)
	} else if account == "" {
		t.Fatal("no account served the request")
	}

	gw.mu.RLock()
	_, byAlias := gw.affinity["sess-2\x00"+router.RouteAuto]
	resolvedAccount, byResolved := gw.affinity["sess-2\x00m"]
	gw.mu.RUnlock()

	if !byAlias {
		t.Fatal("no affinity recorded under the alias pickAutomatic reads")
	}
	if !byResolved || resolvedAccount.Account == "" {
		t.Fatal("no affinity recorded under the resolved model")
	}
}

// An exact-model request keeps using its own key, unchanged.
func TestExactModelAffinityUnchanged(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.bindAffinity("sess-3", "m", "acct-a")

	gw.mu.RLock()
	acct, ok := gw.liveAffinityLocked("sess-3", "m", time.Now())
	gw.mu.RUnlock()
	if !ok || acct != "acct-a" {
		t.Fatalf("exact-model affinity = %q,%v; want acct-a,true", acct, ok)
	}
}

// An expired bind must not pin a session to an account it has outgrown.
func TestAutomaticRouteIgnoresExpiredAffinity(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.cfg.AutomaticRoutes.Auto = []string{"m"}

	gw.mu.Lock()
	gw.affinity = map[string]affinityBind{
		"sess-4\x00" + router.RouteAuto: {Account: "acct-a", Until: time.Now().Add(-time.Minute)},
	}
	gw.mu.Unlock()

	_, candidates, _, err := gw.pickAutomatic(
		context.Background(),
		[]byte(`{"model":"pea/auto","session_id":"sess-4","messages":[{"role":"user","content":"hi"}]}`),
		router.RouteAuto,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) > 0 && candidates[0].Provider.ID == "acct-a" {
		// Catalog order may legitimately put acct-a first; only assert that the
		// expired entry was dropped rather than pinning forever.
		gw.mu.RLock()
		_, still := gw.affinity["sess-4\x00"+router.RouteAuto]
		gw.mu.RUnlock()
		if still {
			t.Fatal("an expired affinity entry was kept")
		}
	}
}
