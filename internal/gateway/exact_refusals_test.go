package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/usage"
)

// optimization.freeOnly is a statement about money: "never spend mine". It was
// consulted only when PeaProxy picked the deployment itself, so a client that
// named the model outright -- or simply had automaticRoutes off -- walked past
// the one switch a user set to protect their account.
func TestFreeOnlyAppliesToAnExplicitlyNamedModel(t *testing.T) {
	paid := 4.0
	hits := 0
	gw := twoAccountGateway(t, countOK(&hits, "a"), countOK(new(int), "b"))
	gw.cfg.Optimization.FreeOnly = boolp(true)
	gw.models = []catalog.Model{{
		ID: "m", AccountID: "acct-a", Tier: catalog.TierPaid,
		Price: catalog.Price{Input: &paid, Output: &paid, Currency: "USD", Source: "openrouter", Verified: true},
	}}

	_, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if err == nil {
		t.Fatal("a metered deployment answered a request that freeOnly forbids spending on")
	}
	if hits != 0 {
		t.Fatalf("the upstream was called %d times; a refused request must not be sent", hits)
	}
	if !mentionsFreeOnly(err.Error()) {
		t.Fatalf("the refusal does not say freeOnly is why: %v", err)
	}
}

// The mirror case: freeOnly must not break the deployments that genuinely cost
// nothing, including a local one, which is not on anyone's bill.
func TestFreeOnlyStillServesAnExplicitFreeModel(t *testing.T) {
	zero := 0.0
	hits := 0
	gw := twoAccountGateway(t, countOK(&hits, "a"), countOK(new(int), "b"))
	gw.cfg.Optimization.FreeOnly = boolp(true)
	gw.models = []catalog.Model{{
		ID: "m", AccountID: "acct-a", Tier: catalog.TierFree,
		Price: catalog.Price{Input: &zero, Output: &zero, Currency: "USD", Source: "openrouter", Verified: true},
	}}

	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatalf("freeOnly refused a verified free deployment: %v", err)
	}
}

// A local model runs on this machine. Charging it against a spend ceiling would
// refuse requests that cannot cost money, which is the ceiling breaking a
// promise it was set to keep rather than enforcing one.
func TestSpendCeilingDoesNotRefuseALocalModel(t *testing.T) {
	hits := 0
	gw := twoAccountGateway(t, countOK(&hits, "a"), countOK(new(int), "b"))
	gw.cfg.Optimization.SpendCeilingUSD = 0.01
	gw.models = []catalog.Model{
		{ID: "m", AccountID: "acct-a", Tier: catalog.TierLocal},
		{ID: "m", AccountID: "acct-b", Tier: catalog.TierLocal},
	}

	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatalf("the spend ceiling refused a local model: %v", err)
	}
}

// A local model costs nothing, so neither guard may touch it -- and that has to
// hold for freeOnly too, even though a local deployment has no price with which
// to prove anything. Refusing it would break the one account that can never bill.
func TestFreeOnlyStillServesALocalModel(t *testing.T) {
	hits := 0
	gw := twoAccountGateway(t, countOK(&hits, "a"), countOK(new(int), "b"))
	gw.cfg.Optimization.FreeOnly = boolp(true)
	gw.models = []catalog.Model{
		{ID: "m", AccountID: "acct-a", Tier: catalog.TierLocal},
		{ID: "m", AccountID: "acct-b", Tier: catalog.TierLocal},
	}

	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatalf("freeOnly refused a local model: %v", err)
	}
	if hits == 0 {
		t.Fatal("the local deployment was never called")
	}
}

// The ceiling is a refusal to spend. An exact model must not be a way around it.
func TestSpendCeilingAppliesToAnExplicitlyNamedModel(t *testing.T) {
	in, out := 3.0, 15.0
	hits := 0
	gw := twoAccountGateway(t, countOK(&hits, "a"), countOK(new(int), "b"))
	gw.cfg.Optimization.SpendCeilingUSD = 1
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"acct-a/m": {Input: &in, Output: &out, Verified: true},
	}
	gw.models = []catalog.Model{{ID: "m", AccountID: "acct-a", Tier: catalog.TierPaid}}
	store := usage.Open("")
	gw.SetUsage(store)
	store.Add(usage.Event{CostUSD: f64(5), Time: time.Now()})

	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)); err == nil {
		t.Fatal("spend already measured past the ceiling, and the request went anyway")
	}
	if hits != 0 {
		t.Fatalf("the upstream was called %d times past the ceiling", hits)
	}
}

// With no ceiling and no freeOnly, an exact model must behave exactly as it did
// before v3.0.4. The whole feature is opt-in in effect, and a release that
// quietly refused named models would be a worse outage than the bug it fixed.
func TestExactRoutingIsUntouchedWithoutTheGuards(t *testing.T) {
	hits := 0
	gw := twoAccountGateway(t, countOK(&hits, "a"), countOK(new(int), "b"))
	gw.models = []catalog.Model{{ID: "m", AccountID: "acct-a", Tier: catalog.TierPaid}}

	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatalf("an exact model was refused with no ceiling and no freeOnly: %v", err)
	}
	if hits == 0 {
		t.Fatal("the request never reached the upstream")
	}
}

// mentionsFreeOnly checks the refusal names the setting the user turned on. A
// refusal the user cannot trace back to their own configuration is a support
// ticket, not a feature.
func mentionsFreeOnly(msg string) bool { return strings.Contains(msg, "freeOnly") }

func f64(v float64) *float64 { return &v }
