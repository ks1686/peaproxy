package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/usage"
)

// blockingGateway serves one upstream. The first chat call parks until released,
// which is how a request is held at the provider with its spend committed; later
// calls answer immediately, so a request that should have been refused cannot
// hang the test when it is not. requests counts upstream calls.
type blockingGateway struct {
	gw       *Gateway
	store    *usage.Store
	requests func() int
	release  chan struct{}
	arrived  chan struct{}
	stop     sync.Once
}

func newBlockingGateway(t *testing.T, inputRate float64, ceiling float64) *blockingGateway {
	t.Helper()
	b := &blockingGateway{
		release: make(chan struct{}),
		arrived: make(chan struct{}, 8),
	}
	var mu sync.Mutex
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		mu.Lock()
		hits++
		first := hits == 1
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],` +
			`"usage":{"prompt_tokens":100,"completion_tokens":10}}`))
		if !first {
			return
		}
		select {
		case b.arrived <- struct{}{}:
		default:
		}
		<-b.release
	}))
	t.Cleanup(up.Close)
	t.Cleanup(func() { b.unblock() })

	in, out := inputRate, inputRate
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Providers: []config.Provider{{
			ID: "acct", Adapter: "openai_compat", Tier: "paid", BaseURL: up.URL + "/v1",
		}},
		AutomaticRoutes: config.AutomaticRoutePrefs{
			Prices: map[string]config.PriceQuote{"acct/m": {Input: &in, Output: &out, Verified: true}},
		},
		Optimization: config.OptimizationPrefs{SpendCeilingUSD: ceiling},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.models = []catalog.Model{{ID: "m", AccountID: "acct", Tier: catalog.TierPaid, Routable: true}}
	store := usage.Open("")
	gw.SetUsage(store)
	gw.Refresh(context.Background())
	b.gw, b.store = gw, store
	b.requests = func() int { mu.Lock(); defer mu.Unlock(); return hits }
	return b
}

func (b *blockingGateway) body(size int) []byte {
	return []byte(`{"model":"m","messages":[{"role":"user","content":"` +
		strings.Repeat("x", size) + `"}]}`)
}

// unblock lets the parked request finish, whichever way the test exits.
func (b *blockingGateway) unblock() {
	b.stop.Do(func() { close(b.release) })
}

// A ceiling is a promise about money, and a burst of concurrent requests used to
// break it: each one read the same recorded spend, each one passed the check, and
// all of them went upstream. The first request here is held open at the provider
// with its reservation taken, so the second is the case that used to overshoot.
func TestCeilingStopsConcurrentRequestsOvershooting(t *testing.T) {
	b := newBlockingGateway(t, 100, 1.5) // 100 USD/M input, ceiling 1.5

	done := make(chan error, 1)
	go func() {
		_, _, err := b.gw.Chat(context.Background(), b.body(10_000))
		done <- err
	}()
	<-b.arrived // the first request is upstream and holds its reservation

	// The ledger has measured nothing yet, because nothing has completed. Only
	// the reservation stands between this request and the ceiling.
	if b.store.Reserved() <= 0 {
		t.Fatal("an in-flight request held no reservation; concurrent requests can overshoot")
	}

	_, _, err := b.gw.Chat(context.Background(), b.body(10_000))
	if err == nil {
		t.Fatal("the second request was allowed past a ceiling the first request had already committed to")
	}
	if !strings.Contains(err.Error(), "refusing to spend") {
		t.Fatalf("refusal does not explain itself: %v", err)
	}
	if got := b.requests(); got != 1 {
		t.Fatalf("upstream saw %d requests, want 1; a refused request must not be sent", got)
	}

	b.unblock()
	if err := <-done; err != nil {
		t.Fatalf("the in-flight request failed: %v", err)
	}
}

// The hold outlives the call, and is released by the event that records what the
// call cost.
//
// Releasing it when the gateway returned left a window: the estimate had left
// reserved and the measured cost had not yet landed, so for that window the
// spend was in neither total and a concurrent request could pass a ceiling it
// should have been refused by. That window is the whole gap.
func TestAHoldIsReleasedByItsEventNotByTheCallReturning(t *testing.T) {
	b := newBlockingGateway(t, 100, 1.5)
	done := make(chan error, 1)
	go func() {
		_, _, err := b.gw.Chat(context.Background(), b.body(10_000))
		done <- err
	}()
	<-b.arrived
	held := b.store.Reserved()
	if held <= 0 {
		t.Fatal("nothing was held while the request was upstream")
	}
	b.unblock()
	if err := <-done; err != nil {
		t.Fatalf("the in-flight request failed: %v", err)
	}

	// The call is over. Nothing has been recorded yet, so the reservation is
	// still doing its job: it is the only thing standing between the next
	// request and a ceiling that would read as untouched.
	if got := b.store.Reserved(); got != held {
		t.Fatalf("the hold was released when the call returned: reserved %v, want %v.\n"+
			"Between here and the event, the spend is in neither total.", got, held)
	}

	// Recording the call pays the hold back.
	cost := 0.001
	b.store.Add(usage.Event{
		AccountID: "acct", Model: "m", PromptTokens: 100, CompletionTokens: 10,
		TokensKnown: true, Costable: true, CostUSD: &cost,
	})
	if got := b.store.Reserved(); got != 0 {
		t.Fatalf("the event did not release the hold: reserved %v, want 0", got)
	}

	// The recorded call is priced, so the ceiling can read it. The next request
	// is allowed because the spend is real and known, not because the machinery
	// lost track of it.
	_, _, err := b.gw.Chat(context.Background(), b.body(10))
	if err != nil {
		t.Fatalf("a settled reservation still blocked the next request: %v", err)
	}
}

// Without a ceiling nothing changes, and a request under the default settings
// must not be held, refused, or slowed by machinery nobody asked for.
func TestNoCeilingMeansNoReservation(t *testing.T) {
	b := newBlockingGateway(t, 100, 0)
	go func() { _, _, _ = b.gw.Chat(context.Background(), b.body(10_000)) }()
	<-b.arrived
	defer b.unblock()

	if got := b.store.Reserved(); got != 0 {
		t.Fatalf("no ceiling is configured, yet %v is reserved", got)
	}
}

// A deployment whose price cannot be quoted reserves nothing rather than
// guessing. The ceiling has already refused such a deployment during routing
// when it is measurable at all; what must not happen here is a fabricated
// figure sitting in the ledger as if it were a measurement.
func TestUnpricedDeploymentReservesNothing(t *testing.T) {
	b := newBlockingGateway(t, 100, 1.5)
	// Drop the configured quote: the account still serves the model, but nothing
	// states what it costs.
	b.gw.cfg.AutomaticRoutes.Prices = nil

	go func() { _, _, _ = b.gw.Chat(context.Background(), b.body(10_000)) }()
	<-b.arrived
	defer b.unblock()

	if got := b.store.Reserved(); got != 0 {
		t.Fatalf("an unquoted deployment reserved %v", got)
	}
}
