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
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/contextstore"
	"github.com/ks1686/peaproxy/internal/usage"
)

// searchingGateway answers the first request with a pea_search tool call and the
// second with the final answer, which is the shape that costs two upstream calls
// and returns one response.
func searchingGateway(t *testing.T, usageBody string) (*Gateway, *usage.Store, func() int) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[` +
				`{"id":"call_1","type":"function","function":{"name":"pea_search","arguments":"{\"query\":\"anything\"}"}}]}}],` +
				`"usage":` + usageBody + `}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}],"usage":` + usageBody + `}`))
	}))
	t.Cleanup(up.Close)

	in, out := 3.0, 15.0
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
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	store := usage.Open("")
	gw.SetUsage(store)
	gw.Artifacts = contextstore.New(contextstore.Options{})
	gw.cfg.Optimization.Automatic = boolp(true)
	gw.Refresh(context.Background())
	return gw, store, func() int { mu.Lock(); defer mu.Unlock(); return calls }
}

// A retrieval round is a real upstream call that a provider bills. PeaProxy
// answered its own tool between rounds, so the round's response was read,
// searched against, and then thrown away -- taking its usage with it. The ledger
// saw one call where two were paid for.
func TestRetrievalRoundsReachTheLedger(t *testing.T) {
	body := `{"prompt_tokens":1000,"completion_tokens":100}`
	gw, store, calls := searchingGateway(t, body)

	resp, _, err := gw.Chat(context.Background(), []byte(
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if calls() != 2 {
		t.Fatalf("upstream saw %d calls, want 2; the round did not happen", calls())
	}
	if !strings.Contains(string(resp.Raw), "done") {
		t.Fatalf("the final answer was not returned: %s", resp.Raw)
	}

	events := store.Recent()
	if len(events) != 1 {
		t.Fatalf("the ledger holds %d events, want 1 recorded for the discarded round", len(events))
	}
	e := events[0]
	if e.AccountID != "acct" || e.Model != "m" {
		t.Fatalf("the round was recorded against the wrong deployment: %+v", e)
	}
	if !e.TokensKnown || !e.Costable {
		t.Fatalf("the round's usage was not read: %+v", e)
	}
	if e.EstimatedUSD == nil {
		t.Fatal("the round was recorded with no cost; it was paid for like any other call")
	}
	want := 1000*3.0/1e6 + 100*15.0/1e6
	if diff := *e.EstimatedUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("round cost = %v, want %v", *e.EstimatedUSD, want)
	}
	w := store.SpentInLastDays(30)
	if w.Priced != 1 {
		t.Fatalf("ledger priced %d calls, want the discarded round counted", w.Priced)
	}
	if diff := w.USD - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("ledger = %v, want %v; the discarded round is not counted", w.USD, want)
	}
}

// A request that searches once and is answered by the same round's follow-up
// must not double-count: the round that is returned to the client is recorded by
// the ordinary path, and only the discarded one is recorded here.
func TestOnlyDiscardedRoundsAreRecorded(t *testing.T) {
	// The upstream answers the first request with a plain answer, so no round is
	// discarded and nothing extra belongs in the ledger from this path.
	var mu sync.Mutex
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":1}}`))
	}))
	t.Cleanup(up.Close)
	in, out := 3.0, 15.0
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
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	store := usage.Open("")
	gw.SetUsage(store)
	gw.Artifacts = contextstore.New(contextstore.Options{})
	gw.cfg.Optimization.Automatic = boolp(true)
	gw.Refresh(context.Background())

	if _, _, err := gw.Chat(context.Background(), []byte(
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`)); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Recent()); got != 0 {
		t.Fatalf("a request with no discarded round recorded %d events", got)
	}
}

var _ = json.Marshal
