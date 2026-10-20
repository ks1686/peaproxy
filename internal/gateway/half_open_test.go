package gateway

import (
	"context"
	"testing"
	"time"
)

func countID(cands []instance, id string) int {
	n := 0
	for _, in := range cands {
		if in.Provider.ID == id {
			n++
		}
	}
	return n
}

// Only the request that pruned an expired cooldown used to ask for the
// half-open probe; every request after it found no cooldown entry and went
// straight to the recovering account. While one attempt probes it, the rest
// move on.
func TestOnlyOneRequestProbesARecoveringAccount(t *testing.T) {
	a, b := 0, 0
	gw := twoAccountGateway(t, countOK(&a, "a"), countOK(&b, "b"))
	gw.cool["acct-a"] = cooldownSlots{wide: Cooldown{AccountID: "acct-a", Until: time.Now().Add(-time.Millisecond)}}
	if first, _ := gw.candidates("m", ""); countID(first, "acct-a") != 1 {
		t.Fatalf("the first request after expiry should be able to probe acct-a: %v", ids(first))
	}
	release, err := gw.admit(context.Background(), "acct-a")
	if err != nil {
		t.Fatalf("the first attempt could not take the probe: %v", err)
	}
	if second, _ := gw.candidates("m", ""); countID(second, "acct-a") != 0 {
		t.Fatalf("a second request listed acct-a while its probe is out: %v", ids(second))
	}
	if _, err := gw.admit(context.Background(), "acct-a"); !admissionRejection(err) {
		t.Fatalf("a second attempt on acct-a while it is probed: %v, want an admission refusal", err)
	}
	gw.rememberSuccess("", "m", "acct-a")
	release()
	for i := 0; i < 2; i++ {
		if cands, _ := gw.candidates("m", ""); countID(cands, "acct-a") != 1 {
			t.Fatalf("after a successful probe acct-a is healthy again: %v", ids(cands))
		}
		release, err := gw.admit(context.Background(), "acct-a")
		if err != nil {
			t.Fatalf("a recovered account needed another probe: %v", err)
		}
		release()
	}
}

// Listing a recovering account is not trying it. A request served by another
// account must not leave the probe held, or every other request loses the
// account (and its failover) until the probe expires.
func TestListingARecoveringAccountDoesNotHoldItsProbe(t *testing.T) {
	a, b := 0, 0
	gw := twoAccountGateway(t, countOK(&a, "a"), countOK(&b, "b"))
	gw.cool["acct-a"] = cooldownSlots{wide: Cooldown{AccountID: "acct-a", Until: time.Now().Add(-time.Millisecond)}}
	for i := 0; i < 3; i++ {
		if cands, _ := gw.candidates("m", ""); countID(cands, "acct-a") != 1 {
			t.Fatalf("request %d lost acct-a although nobody is trying it: %v", i+1, ids(cands))
		}
	}
}

// ExplainRoute is read-only. It used to share candidates() with requests, so
// explaining a route pruned the cooldown and took the half-open probe, and the
// next real request found the account "recovering".
func TestExplainingARouteDoesNotTakeTheProbe(t *testing.T) {
	a, b := 0, 0
	gw := twoAccountGateway(t, countOK(&a, "a"), countOK(&b, "b"))
	gw.cool["acct-a"] = cooldownSlots{wide: Cooldown{AccountID: "acct-a", Until: time.Now().Add(-time.Millisecond)}}
	_ = gw.ExplainRoute(context.Background(), "m")
	cands, _ := gw.candidates("m", "")
	for _, in := range cands {
		if in.Provider.ID == "acct-a" {
			return
		}
	}
	t.Fatalf("after an explain, the first real request could not probe acct-a: %v", ids(cands))
}

func ids(cands []instance) []string {
	out := make([]string, len(cands))
	for i, in := range cands {
		out[i] = in.Provider.ID
	}
	return out
}

// An attempt that neither succeeds nor cools the account (a cancelled or
// client-fault request) still ends the probe when its slot is released.
func TestAProbeEndsWithItsAttempt(t *testing.T) {
	a, b := 0, 0
	gw := twoAccountGateway(t, countOK(&a, "a"), countOK(&b, "b"))
	gw.cool["acct-a"] = cooldownSlots{wide: Cooldown{AccountID: "acct-a", Until: time.Now().Add(-time.Millisecond)}}
	_, _ = gw.candidates("m", "")
	release, err := gw.admit(context.Background(), "acct-a")
	if err != nil {
		t.Fatal(err)
	}
	if !gw.admission.Probing("acct-a") {
		t.Fatal("setup: the attempt did not take the probe")
	}
	release()
	if gw.admission.Probing("acct-a") {
		t.Fatal("the probe outlived its attempt; acct-a stays out of rotation until it expires")
	}
}

func TestExplainingARouteLeavesModelCooldownsAlone(t *testing.T) {
	a, b := 0, 0
	gw := twoAccountGateway(t, countOK(&a, "a"), countOK(&b, "b"))
	gw.cool["acct-a"] = cooldownSlots{models: map[string]Cooldown{
		"m":     {AccountID: "acct-a", Until: time.Now().Add(-time.Millisecond)},
		"other": {AccountID: "acct-a", Until: time.Now().Add(time.Hour)},
	}}
	_ = gw.ExplainRoute(context.Background(), "m")
	if _, ok := gw.cool["acct-a"].models["m"]; !ok {
		t.Fatal("explaining a route deleted an expired model cooldown; explain is read-only")
	}
}
