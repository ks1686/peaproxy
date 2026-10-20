package gateway

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/usage"
)

// coalescingGateway serves one priced paid account whose chat answer waits for
// release, with the response cache (and so coalescing) and a spend ceiling on.
func coalescingGateway(t *testing.T) (*Gateway, *usage.Store, chan struct{}, *atomic.Int32) {
	t.Helper()
	release := make(chan struct{})
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		calls.Add(1)
		<-release
		_, _ = w.Write([]byte(`{"id":"c","model":"m","choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":100}}`))
	}))
	t.Cleanup(up.Close)
	// Runs before up.Close (cleanups are LIFO), so a failed test does not hang
	// on a handler still waiting.
	t.Cleanup(func() { unblock(release) })
	in, out := 3.0, 15.0
	cfg := config.Config{
		SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317,
		Providers:       []config.Provider{{ID: "acct", Adapter: "openai_compat", Tier: "paid", BaseURL: up.URL + "/v1"}},
		AutomaticRoutes: config.AutomaticRoutePrefs{Prices: map[string]config.PriceQuote{"acct/m": {Input: &in, Output: &out, Verified: true}}},
	}
	cfg.RequestEngine.CacheResponses = true
	cfg.Optimization.SpendCeilingUSD = 100
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	store := usage.Open("")
	gw.SetUsage(store)
	gw.Refresh(context.Background())
	return gw, store, release, &calls
}

const coalescedBody = `{"model":"m","temperature":0,"messages":[{"role":"user","content":"same"}]}`

// record is what the server does after a request: one event that carries the
// request's holds and, on success, the usage the response published.
func recordAs(store *usage.Store, ctx context.Context, body []byte) {
	e := usage.Event{AccountID: "acct", Model: "m", Holds: usage.HoldsFrom(ctx).Take(), HoldsTracked: true}
	if body != nil {
		usage.ApplyPublishedUsage(&e, body, false)
	}
	store.Add(e)
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The coalesced call took its hold into the leader's set. A leader that left
// recorded its error event and released the hold while the call was still
// running for the follower, which was then billed with no hold to release.
func TestACoalescedCallKeepsItsHoldUntilTheBilledReceiverRecords(t *testing.T) {
	gw, store, release, calls := coalescingGateway(t)
	leaderCtx, leave := context.WithCancel(usage.WithHolds(context.Background()))
	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := gw.Chat(leaderCtx, []byte(coalescedBody))
		leaderDone <- err
	}()
	waitUntil(t, func() bool { return calls.Load() == 1 })
	held := store.Reserved()
	if held <= 0 {
		t.Fatal("setup: the call took no hold")
	}
	followerCtx := usage.WithHolds(context.Background())
	type result struct {
		body []byte
		err  error
	}
	followerDone := make(chan result, 1)
	go func() {
		resp, _, err := gw.Chat(followerCtx, []byte(coalescedBody))
		followerDone <- result{resp.Raw, err}
	}()
	time.Sleep(100 * time.Millisecond) // let the follower join the flight
	leave()
	if err := <-leaderDone; err == nil {
		t.Fatal("the leader left but got an answer")
	}
	recordAs(store, leaderCtx, nil)
	if got := store.Reserved(); math.Abs(got-held) > 1e-12 {
		t.Fatalf("reserved while the call is still running = %v, want its hold %v kept", got, held)
	}
	unblock(release)
	r := <-followerDone
	if r.err != nil {
		t.Fatal(r.err)
	}
	recordAs(store, followerCtx, r.body)
	if got := store.Reserved(); math.Abs(got) > 1e-12 {
		t.Fatalf("reserved after the billed follower recorded = %v, want 0", got)
	}
}

// Everyone who waited on a coalesced call left, so the flight cancelled it. The
// call's hold has to go with it, not wait out its expiry.
func TestACoalescedCallEveryoneLeftReleasesItsHold(t *testing.T) {
	gw, store, _, calls := coalescingGateway(t)
	ctx, leave := context.WithCancel(usage.WithHolds(context.Background()))
	done := make(chan struct{})
	go func() {
		_, _, _ = gw.Chat(ctx, []byte(coalescedBody))
		close(done)
	}()
	waitUntil(t, func() bool { return calls.Load() == 1 })
	if store.Reserved() <= 0 {
		t.Fatal("setup: the call took no hold")
	}
	leave()
	<-done
	recordAs(store, ctx, nil)
	waitUntil(t, func() bool { return math.Abs(store.Reserved()) < 1e-12 })
}

func unblock(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}
