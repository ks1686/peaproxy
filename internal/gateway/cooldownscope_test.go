package gateway

import (
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
)

// #73: each account had one cooldown slot. A model-scoped failure replaced
// whatever was there, including a cooldown for the whole account or for a
// different model -- so a 401 that cooled the account was undone by a 429 on
// another model.

func cooldownGateway(t *testing.T) *Gateway {
	t.Helper()
	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{ID: "a", Adapter: "openai_compat", Tier: "paid", BaseURL: "http://127.0.0.1:1/v1"},
		{ID: "b", Adapter: "openai_compat", Tier: "paid", BaseURL: "http://127.0.0.1:2/v1"},
	}
	g, err := New(cfg, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// cooldownCause only speaks about accounts the catalog lists for the model,
	// so the fixture needs a catalog.
	g.mu.Lock()
	for _, id := range []string{"model-one", "model-two"} {
		g.models = append(g.models, catalog.Model{ID: id, AccountID: "a", Tier: "paid"})
	}
	g.mu.Unlock()
	return g
}

func accountWide(retryAfter time.Duration) error {
	return adapter.HTTPError{Status: 401, Body: "bad key", RetryAfter: retryAfter, Scope: adapter.ScopeAccount}
}

func modelScoped(retryAfter time.Duration) error {
	return adapter.HTTPError{Status: 429, Body: "slow down", RetryAfter: retryAfter, Scope: adapter.ScopeModel}
}

func (g *Gateway) coolingFor(model string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for id := range g.cool {
		if c, ok := activeCooldown(g.cool[id], model, now); ok && now.Before(c.Until) {
			return true
		}
	}
	return false
}

func TestAModelScopedCooldownDoesNotReplaceAnAccountWideOne(t *testing.T) {
	g := cooldownGateway(t)
	g.markCooldown("a", "model-one", accountWide(time.Minute))
	if !g.coolingFor("model-one") {
		t.Fatal("the account-wide cooldown was not recorded")
	}
	// A 429 scoped to a different model must not narrow it away.
	g.markCooldown("a", "model-two", modelScoped(time.Minute))
	if !g.coolingFor("model-one") {
		t.Error("a model-scoped cooldown on another model replaced the account-wide one; model-one became eligible again")
	}
	if !g.coolingFor("model-two") {
		t.Error("the model-scoped cooldown was not recorded at all")
	}
}

func TestTwoModelScopedCooldownsCoexist(t *testing.T) {
	g := cooldownGateway(t)
	g.markCooldown("a", "model-one", modelScoped(time.Minute))
	g.markCooldown("a", "model-two", modelScoped(2*time.Minute))
	for _, m := range []string{"model-one", "model-two"} {
		if !g.coolingFor(m) {
			t.Errorf("%s is not cooling: the second model-scoped cooldown replaced the first", m)
		}
	}
}

func TestAModelScopedCooldownDoesNotCoolOtherModels(t *testing.T) {
	g := cooldownGateway(t)
	g.markCooldown("a", "model-one", modelScoped(time.Minute))
	if g.coolingFor("model-two") {
		t.Error("a model-scoped cooldown leaked to a different model")
	}
}

// An expired cooldown must not keep the account out, whichever slot it is in.
func TestExpiredCooldownsDoNotBlock(t *testing.T) {
	g := cooldownGateway(t)
	g.markCooldown("a", "model-one", accountWide(10*time.Millisecond))
	g.markCooldown("a", "model-two", modelScoped(10*time.Millisecond))
	time.Sleep(30 * time.Millisecond)
	if g.coolingFor("model-one") || g.coolingFor("model-two") {
		t.Error("an expired cooldown still blocks the account")
	}
}

// Health reports an account as cooling when any cooldown is active for it, and
// the reported reason should be one a person can act on.
func TestHealthReportsCooldownWhileAnySlotIsActive(t *testing.T) {
	g := cooldownGateway(t)
	g.markCooldown("a", "model-one", modelScoped(time.Minute))
	var found bool
	for _, c := range g.Cooldowns() {
		if c.AccountID == "a" && c.Model == "model-one" {
			found = true
		}
	}
	if !found {
		t.Errorf("the health list does not report the model-scoped cooldown: %+v", g.Cooldowns())
	}
}

// The cooldownCause message an account gets when it is the only one left should
// name the account, and a cooldown for a different model is not a reason to
// hold up this request.
func TestCooldownCauseIgnoresAnotherModelsCooldown(t *testing.T) {
	g := cooldownGateway(t)
	g.markCooldown("a", "model-one", modelScoped(time.Minute))
	if err := g.cooldownCause("model-two"); err != nil {
		t.Errorf("a cooldown for model-one blocked model-two: %v", err)
	}
	if err := g.cooldownCause("model-one"); err == nil {
		t.Error("model-one is cooling but no cause was reported")
	}
}
