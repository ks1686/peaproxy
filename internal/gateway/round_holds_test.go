package gateway

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/contextstore"
	"github.com/ks1686/peaproxy/internal/usage"
)

// A retrieval round PeaProxy discards is recorded with its real cost. Its hold
// has to leave with it, or the next round is admitted against both the
// finished round's hold and its recorded cost.
func TestADiscardedRoundReleasesItsSpendHold(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	var store *usage.Store
	var reservedPerCall []float64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		mu.Lock()
		calls++
		n := calls
		reservedPerCall = append(reservedPerCall, store.Reserved())
		mu.Unlock()
		if n == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"pea_search","arguments":"{\"query\":\"q\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
	}))
	t.Cleanup(up.Close)
	in, out := 3.0, 15.0
	cfg := config.Config{
		SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317,
		Providers:       []config.Provider{{ID: "acct", Adapter: "openai_compat", Tier: "paid", BaseURL: up.URL + "/v1"}},
		AutomaticRoutes: config.AutomaticRoutePrefs{Prices: map[string]config.PriceQuote{"acct/m": {Input: &in, Output: &out, Verified: true}}},
	}
	cfg.Optimization.SpendCeilingUSD = 100
	cfg.Optimization.Automatic = boolp(true)
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	store = usage.Open("")
	gw.SetUsage(store)
	gw.Artifacts = contextstore.New(contextstore.Options{})
	gw.Refresh(context.Background())
	ctx := usage.WithHolds(context.Background())
	if _, _, err := gw.Chat(ctx, []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`)); err != nil {
		t.Fatal(err)
	}
	if len(reservedPerCall) != 2 {
		t.Fatalf("upstream calls = %d, want 2 (search round + answer)", len(reservedPerCall))
	}
	first, second := reservedPerCall[0], reservedPerCall[1]
	if first <= 0 {
		t.Fatal("no hold was taken for the first round; the test proves nothing")
	}
	// Round 2's body carries round 1's, so its own hold is at least round 1's;
	// still holding round 1 as well puts the total at twice that or more.
	if second >= 2*first {
		t.Fatalf("reserved during round 2 = %v, round 1 held %v: the discarded round's hold was not released", second, first)
	}
	// The server records the request's own event, which takes what is left.
	store.Add(usage.Event{AccountID: "acct", Model: "m", Holds: usage.HoldsFrom(ctx).Take(), HoldsTracked: true})
	if math.Abs(store.Reserved()) > 1e-12 {
		t.Fatalf("reserved after the request's event = %v, want 0", store.Reserved())
	}
}
