package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
	"github.com/ks1686/peaproxy/internal/usage"
)

// ledgerServer serves one upstream that publishes token counts and no cost,
// which is what most providers actually do.
func ledgerServer(t *testing.T, usageBody string, cache float64) (*Server, *usage.Store) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list", "data": []map[string]string{{"id": "m1"}},
			})
		case "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    "chatcmpl-test",
				"model": "m1",
				"choices": []map[string]any{{
					"message":       map[string]string{"role": "assistant", "content": "hi"},
					"finish_reason": "stop",
				}},
				"usage": json.RawMessage(usageBody),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)

	in, out := 3.0, 15.0
	quote := config.PriceQuote{Input: &in, Output: &out, Verified: true}
	if cache > 0 {
		quote.CacheRead = &cache
	}
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Providers: []config.Provider{{
			ID: "acct", Adapter: "openai_compat", Tier: "paid", BaseURL: up.URL + "/v1",
		}},
		AutomaticRoutes: config.AutomaticRoutePrefs{
			Prices: map[string]config.PriceQuote{
				"acct/m1": quote,
			},
		},
	}
	gw, err := gateway.New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	store := usage.Open("")
	gw.SetUsage(store)
	gw.Refresh(context.Background())
	return New(Options{Gateway: gw}), store
}

func postChat(t *testing.T, s *Server) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`))
	req.RemoteAddr = "127.0.0.1:4321"
	req.Host = "127.0.0.1:8317"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
}

// The largest gap v3.0.1 left open: a provider that publishes token counts and no
// cost left every call unpriced, so a ceiling set against such a provider could
// never be satisfied and refused every paid request forever. PeaProxy knows the
// deployment's quote, so it can price the call itself.
func TestSpentLedgerIsPricedFromPublishedTokens(t *testing.T) {
	s, store := ledgerServer(t, `{"prompt_tokens":1000,"completion_tokens":100}`, 0)
	postChat(t, s)

	events := store.Recent()
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}
	e := events[0]
	if e.CostUSD != nil {
		t.Fatalf("provider cost = %v, want nil; it published none", *e.CostUSD)
	}
	if e.EstimatedUSD == nil {
		t.Fatal("no cost and no estimate: the call is unmeasurable and a ceiling can never trust the ledger")
	}
	want := 1000*3.0/1e6 + 100*15.0/1e6
	if diff := *e.EstimatedUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("estimated = %v, want %v", *e.EstimatedUSD, want)
	}

	w := store.SpentInLastDays(30)
	if w.Priced != 1 || w.Total != 1 {
		t.Fatalf("priced=%d total=%d, want 1 of 1", w.Priced, w.Total)
	}
	if w.EstimatedUSD != *e.EstimatedUSD {
		t.Fatalf("window estimated = %v, want %v", w.EstimatedUSD, *e.EstimatedUSD)
	}
}

// OpenAI counts cached tokens inside the prompt total. Billing the prompt total
// and the cached portion as separate amounts charges those tokens twice, so the
// estimate has to price 200 at the input rate and 800 at the cache rate.
func TestEstimatedSpendPricesNestedCacheReadsOnce(t *testing.T) {
	s, store := ledgerServer(t, `{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800}}`, 0.3)
	postChat(t, s)

	e := store.Recent()[0]
	if e.EstimatedUSD == nil {
		t.Fatal("no estimate for a call whose components are all quoted")
	}
	want := 200*3.0/1e6 + 800*0.3/1e6 + 100*15.0/1e6
	if diff := *e.EstimatedUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("estimated = %v, want %v (the cached tokens are charged once, at the cache rate)", *e.EstimatedUSD, want)
	}
}

// A cached call on a quote with no cache rate cannot be priced. Recording the
// input side alone would understate the bill, which is the direction that lets a
// ceiling pass spend it should have refused.
func TestACachedCallOnAnUnquotedRateStaysUnmeasured(t *testing.T) {
	s, store := ledgerServer(t, `{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800}}`, 0)
	postChat(t, s)

	e := store.Recent()[0]
	if e.EstimatedUSD != nil {
		t.Fatalf("priced a cached call with no cache rate published: %v", *e.EstimatedUSD)
	}
	w := store.SpentInLastDays(30)
	if w.Priced == w.Total {
		t.Fatal("a call with no published cache rate was recorded as fully priced")
	}
}

// A provider that publishes its own cost is the bill. PeaProxy does not also
// attach an estimate for the same call.
func TestProviderCostIsNotOverwrittenByAnEstimate(t *testing.T) {
	s, store := ledgerServer(t, `{"prompt_tokens":1000,"completion_tokens":100,"cost":0.42}`, 0)
	postChat(t, s)

	e := store.Recent()[0]
	if e.CostUSD == nil || *e.CostUSD != 0.42 {
		t.Fatalf("provider cost = %v, want 0.42", e.CostUSD)
	}
	if e.EstimatedUSD != nil {
		t.Fatalf("an estimate was attached to a call whose provider published its cost: %v", *e.EstimatedUSD)
	}
}

// Usage published on one side only is not a complete measurement. Half a call's
// tokens are still spend, and a total that omits them fails open.
func TestPartialUsageIsNotPriced(t *testing.T) {
	s, store := ledgerServer(t, `{"prompt_tokens":1000}`, 0)
	postChat(t, s)

	if e := store.Recent()[0]; e.EstimatedUSD != nil {
		t.Fatalf("priced a call whose output length was never published: %v", *e.EstimatedUSD)
	}
	w := store.SpentInLastDays(30)
	if w.Priced != 0 || w.Total != 1 {
		t.Fatalf("priced=%d total=%d, want 0 of 1", w.Priced, w.Total)
	}
}

// The panel has to keep the provider's own figures and PeaProxy's estimates
// apart. A single blended number would read as a bill, and a user checking it
// against their provider's invoice would find it wrong without being able to
// see which part was a guess.
func TestAdminPolicyReportsEstimatedSpendSeparately(t *testing.T) {
	s, _ := ledgerServer(t, `{"prompt_tokens":1000,"completion_tokens":100}`, 0)
	postChat(t, s)

	policy := adminGET(t, s, "/admin/policy")["policy"].(map[string]any)
	spent, _ := policy["spentLast30DaysUSD"].(float64)
	estimated, _ := policy["estimatedLast30DaysUSD"].(float64)
	if spent <= 0 {
		t.Fatalf("spent = %v, want the measured total", spent)
	}
	if estimated <= 0 || estimated > spent {
		t.Fatalf("estimated = %v against a spent total of %v; the estimate must be reported, and be part of it", estimated, spent)
	}
	if measurable, _ := policy["spendMeasurable"].(bool); !measurable {
		t.Error("a call priced from its own published tokens is measurable and the panel says otherwise")
	}
	if _, ok := policy["inFlightReservedUSD"]; !ok {
		t.Error("the panel does not report spend held by requests in flight")
	}
}
