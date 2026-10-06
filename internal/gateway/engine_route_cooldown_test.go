package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/router"
)

// An account can hold several active cooldowns at once: one per model plus an
// account-wide one. Ranking used to collapse them with anyActive, which keeps a
// single arbitrary slot, and then tested that one slot against the candidate
// model. A candidate whose own model was cooling was therefore ranked as
// available whenever a *different* model on the same account was also cooling
// and had the sooner expiry.
//
// https://github.com/ks1686/peaproxy plan T1 / defect D4.
func TestAutomaticSkipsAccountWhoseCandidateModelIsCooling(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.cfg.AutomaticRoutes.Auto = []string{"m"}

	gw.cool["acct-a"] = cooldownSlots{models: map[string]Cooldown{
		// Sooner expiry than the one that names the candidate model.
		"other": {AccountID: "acct-a", Model: "other", Until: time.Now().Add(time.Minute)},
		"m":     {AccountID: "acct-a", Model: "m", Until: time.Now().Add(time.Hour)},
	}}

	_, candidates, _, err := gw.pickAutomatic(
		context.Background(),
		[]byte(`{"model":"pea/auto","messages":[{"role":"user","content":"hi"}]}`),
		router.RouteAuto,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range candidates {
		if c.Provider.ID == "acct-a" {
			t.Fatal("acct-a ranked for model m while acct-a was cooling m")
		}
	}
	if len(candidates) == 0 {
		t.Fatal("no candidate remained")
	}
}

// The complementary case: a model-scoped cooldown on another model must not
// take a healthy account out of rotation for a model it can still serve.
func TestAutomaticKeepsAccountCoolingOnlyAnotherModel(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.cfg.AutomaticRoutes.Auto = []string{"m"}

	gw.cool["acct-a"] = cooldownSlots{models: map[string]Cooldown{
		"other": {AccountID: "acct-a", Model: "other", Until: time.Now().Add(time.Hour)},
	}}

	_, candidates, _, err := gw.pickAutomatic(
		context.Background(),
		[]byte(`{"model":"pea/auto","messages":[{"role":"user","content":"hi"}]}`),
		router.RouteAuto,
	)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range candidates {
		if c.Provider.ID == "acct-a" {
			found = true
		}
	}
	if !found {
		t.Fatal("acct-a dropped for model m although only an unrelated model was cooling")
	}
}

// An account-wide cooldown is not model scoped, so it must exclude every model
// on that account regardless of which slot anyActive happened to return.
func TestAutomaticAccountWideCooldownExcludesEveryModel(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.cfg.AutomaticRoutes.Auto = []string{"m"}

	gw.cool["acct-a"] = cooldownSlots{
		wide:   Cooldown{AccountID: "acct-a", Until: time.Now().Add(time.Hour)},
		models: map[string]Cooldown{"zzz": {AccountID: "acct-a", Model: "zzz", Until: time.Now().Add(time.Minute)}},
	}

	_, candidates, _, err := gw.pickAutomatic(
		context.Background(),
		[]byte(`{"model":"pea/auto","messages":[{"role":"user","content":"hi"}]}`),
		router.RouteAuto,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range candidates {
		if c.Provider.ID == "acct-a" {
			t.Fatal("acct-a ranked despite an active account-wide cooldown")
		}
	}
}

// An exhausted free tier does not recover in thirty seconds. Cooling it that
// briefly re-tests a known-bad account every half minute, and on a model served
// by that account alone the user gets the failure over and over instead of a
// cooldown that actually reflects the situation.
func TestEntitlementFailureGetsALongCooldown(t *testing.T) {
	entitlement := adapter.HTTPError{Status: 402, Body: `{"error":{"message":"payment required, free tier exhausted"}}`}
	if got := cooldownFor(entitlement); got != EntitlementCooldownTTL {
		t.Errorf("a 402 entitlement cooled for %s, want %s", got, EntitlementCooldownTTL)
	}
	if EntitlementCooldownTTL <= CooldownTTL {
		t.Error("the entitlement cooldown is not longer than the generic one, so nothing changed")
	}
}

// The long cooldown must not swallow the errors that genuinely do clear in
// seconds.
func TestShortLivedFailuresKeepShortCooldowns(t *testing.T) {
	rateLimit := adapter.HTTPError{Status: 429, Body: `{"error":{"message":"rate_limit_exceeded"}}`}
	if got := cooldownFor(rateLimit); got >= EntitlementCooldownTTL {
		t.Errorf("a rate limit cooled for %s; it is not an entitlement failure", got)
	}
	server := adapter.HTTPError{Status: 500, Body: "internal error"}
	if got := cooldownFor(server); got != CooldownTTL {
		t.Errorf("a 500 cooled for %s, want the generic %s", got, CooldownTTL)
	}
}

// An explicit Retry-After from the provider is the provider telling us when to
// return, and it outranks any opinion of ours.
func TestRetryAfterOutranksOurEntitlementCooldown(t *testing.T) {
	withRetry := adapter.HTTPError{Status: 402, Body: `{"error":{"message":"payment required"}}`, RetryAfter: 90 * time.Second}
	if got := cooldownFor(withRetry); got != 90*time.Second {
		t.Errorf("Retry-After was overridden: got %s, want 90s", got)
	}
}
