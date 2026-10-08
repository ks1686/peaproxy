package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/config"
)

// One exact model is often offered by more than one account. A paid account
// being first in the list said nothing about the free account behind it, and the
// guard refused the whole request on seeing it -- turning "this account costs
// money" into "this model is unusable".
//
// That is the worse claim by a long way: the user has a free account that can
// serve the call, and PeaProxy was refusing to use it.
func TestFreeOnlyPrefersTheFreeAccountWhenOneModelHasBoth(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	zero := 0.0
	one := 1.0
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"acct-a/m": {Input: &one, Output: &one, Verified: true},
		"acct-b/m": {Input: &zero, Output: &zero, Verified: true},
	}
	gw.cfg.Optimization.FreeOnly = boolp(true)
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	model, cands, _, err := gw.routeResolved(context.Background(), body, "m")
	if err != nil {
		t.Fatalf("a free account could serve the model, but the request was refused: %v", err)
	}
	if len(cands) != 1 || cands[0].Provider.ID != "acct-b" {
		t.Fatalf("candidates = %v, want only acct-b", candidateIDs(cands))
	}
	if model != "m" {
		t.Fatalf("model = %q", model)
	}
}

// With every account paid, the refusal is still a refusal, and it still names
// the setting. Filtering must not turn a hard refusal into a silent fallback.
func TestFreeOnlyStillRefusesWhenNoAccountIsFree(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	one := 1.0
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"acct-a/m": {Input: &one, Output: &one, Verified: true},
		"acct-b/m": {Input: &one, Output: &one, Verified: true},
	}
	gw.cfg.Optimization.FreeOnly = boolp(true)
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if _, _, _, err := gw.routeResolved(context.Background(), body, "m"); err == nil {
		t.Fatal("every account was paid and the request was served anyway")
	} else if !strings.Contains(err.Error(), "freeOnly") {
		t.Fatalf("the refusal does not name the setting: %v", err)
	}
}

func candidateIDs(cands []instance) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Provider.ID)
	}
	return out
}
