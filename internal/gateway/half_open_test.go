package gateway

import (
	"context"
	"testing"
	"time"
)

// Only the request that pruned an expired cooldown used to ask for the
// half-open probe; every request after it found no cooldown entry and went
// straight to the recovering account. While one probe is out, the rest wait.
func TestOnlyOneRequestProbesARecoveringAccount(t *testing.T) {
	a, b := 0, 0
	gw := twoAccountGateway(t, countOK(&a, "a"), countOK(&b, "b"))
	gw.cool["acct-a"] = cooldownSlots{wide: Cooldown{AccountID: "acct-a", Until: time.Now().Add(-time.Millisecond)}}
	first, _ := gw.candidates("m", "")
	second, _ := gw.candidates("m", "")
	count := func(cands []instance) int {
		n := 0
		for _, in := range cands {
			if in.Provider.ID == "acct-a" {
				n++
			}
		}
		return n
	}
	if count(first) != 1 {
		t.Fatalf("the first request after expiry should probe acct-a: %v", ids(first))
	}
	if count(second) != 0 {
		t.Fatalf("a second request went to acct-a while its probe is out: %v", ids(second))
	}
	gw.rememberSuccess("", "m", "acct-a")
	if third, _ := gw.candidates("m", ""); count(third) != 1 {
		t.Fatalf("after a successful probe acct-a is healthy again: %v", ids(third))
	}
	if third, _ := gw.candidates("m", ""); count(third) != 1 {
		t.Fatalf("a recovered account must not need another probe: %v", ids(third))
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
	if _, _ = gw.candidates("m", ""); !gw.admission.Probing("acct-a") {
		t.Fatal("setup: the first request did not take the probe")
	}
	release, err := gw.admit(context.Background(), "acct-a")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if gw.admission.Probing("acct-a") {
		t.Fatal("the probe outlived its attempt; acct-a stays out of rotation until it expires")
	}
}
