package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
	"github.com/ks1686/peaproxy/internal/usage"
)

func TestCoalescedRequestsAreBilledOnceAndReleaseTheirHold(t *testing.T) {
	var upstreamCalls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]string{{"id": "m1"}}})
		case "/v1/chat/completions":
			upstreamCalls.Add(1)
			time.Sleep(300 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "c", "model": "m1",
				"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "hi"}, "finish_reason": "stop"}},
				"usage":   map[string]int{"prompt_tokens": 1000, "completion_tokens": 100},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	in, out := 3.0, 15.0
	cfg := config.Config{
		SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317,
		Providers:     []config.Provider{{ID: "acct", Adapter: "openai_compat", Tier: "paid", BaseURL: up.URL + "/v1"}},
		AutomaticRoutes: config.AutomaticRoutePrefs{Prices: map[string]config.PriceQuote{
			"acct/m1": {Input: &in, Output: &out, Verified: true},
		}},
		RequestEngine: config.RequestEnginePrefs{CacheResponses: true},
	}
	cfg.Optimization.SpendCeilingUSD = 100
	gw, err := gateway.New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	store := usage.Open("")
	gw.SetUsage(store)
	gw.Refresh(context.Background())
	s := New(Options{Gateway: gw})

	const n = 4
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"m1","temperature":0,"messages":[{"role":"user","content":"same"}]}`))
			req.RemoteAddr, req.Host = "127.0.0.1:4321", "127.0.0.1:8317"
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			s.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Errorf("status %d: %s", rr.Code, rr.Body)
			}
		}()
	}
	wg.Wait()
	if got := upstreamCalls.Load(); got != 1 {
		t.Skipf("requests were not coalesced (%d upstream calls); nothing to assert about shared billing", got)
	}
	w := store.SpentInLastDays(1)
	want := (1000*in + 100*out) / 1e6
	if w.Total != n || w.Priced != n {
		t.Fatalf("calls %d priced %d, want %d recorded and all priced", w.Total, w.Priced, n)
	}
	if w.USD < want*0.999 || w.USD > want*1.001 {
		t.Fatalf("window spend %v for one upstream call costing %v: shared results were billed again", w.USD, want)
	}
	if got := store.Reserved(); got != 0 {
		t.Fatalf("reserved = %v after every coalesced request finished, want 0", got)
	}
}
