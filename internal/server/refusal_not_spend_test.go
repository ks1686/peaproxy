package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/usage"
)

// postChatRefused sends one request and requires it to be refused, so a test
// about refusals cannot pass by quietly succeeding.
func postChatRefused(t *testing.T, s *Server) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`))
	req.RemoteAddr = "127.0.0.1:4321"
	req.Host = "127.0.0.1:8317"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code == 200 {
		t.Fatalf("the request succeeded; the ceiling did not refuse it")
	}
}

// spendPanel is the spend half of /admin/policy, where a user would look.
func spendPanel(t *testing.T, s *Server) map[string]any {
	t.Helper()
	panel := adminGET(t, s, "/admin/policy")
	inner, ok := panel["policy"].(map[string]any)
	if !ok {
		t.Fatalf("the policy panel has no policy block: %+v", panel)
	}
	return inner
}

// ceilingOn puts the ceiling just under what the seeded call already cost, so
// the next request is refused before dispatch.
func ceilingOn(t *testing.T, s *Server, usd float64) {
	t.Helper()
	cfg := s.gw.Config()
	cfg.Optimization.SpendCeilingUSD = usd
	s.gw.SetConfig(cfg)
}

// A ceiling refusal never reached a provider and cost the user nothing.
//
// Recorded as an ordinary call it inflates the denominator without ever being
// priced, which flips the ceiling from "over budget" to "spend cannot be
// measured" -- and from then on it refuses everything while advising the user to
// set prices they already set.
//
// This goes through the whole path -- request, gateway, refusal, ledger -- because
// the gateway is the only thing that knows a refusal happened before dispatch. An
// event that had refused upstream looks identical from the ledger otherwise: no
// usage either way.
func TestACeilingRefusalIsNotCountedAsUnmeasuredSpend(t *testing.T) {
	body := `{"prompt_tokens":1000,"completion_tokens":100}`
	s, store := ledgerServer(t, body, 0)

	// Spend the whole ceiling in one call.
	cost := 10.0
	store.Add(usage.Event{
		AccountID: "acct", Model: "m1", TokensKnown: true, Costable: true, CostUSD: &cost,
	})
	ceilingOn(t, s, cost*0.5)

	postChatRefused(t, s)

	events := store.Recent()
	if len(events) != 2 {
		t.Fatalf("the ledger holds %d events, want 2: the refusal was not recorded at all", len(events))
	}
	refusal := events[0]
	if refusal.Error == "" {
		t.Fatalf("the refusal was not recorded as a refusal: %+v", refusal)
	}
	if !refusal.NotDispatched {
		t.Fatalf("the refusal was not marked as never dispatched: %+v", refusal)
	}

	// And the window it leaves behind is still measurable, which is the whole
	// point: a refusal that cannot be measured is a ceiling stuck off.
	panel := spendPanel(t, s)
	if panel["spendMeasurable"] != true {
		t.Fatalf("a refusal that never left the machine made the window unmeasurable: %+v", panel)
	}
}

// The panel is where a user would look, so the failure has to be visible there
// and not only in the rollup.
func TestThePanelDoesNotBlameThePricesForARefusal(t *testing.T) {
	body := `{"prompt_tokens":1000,"completion_tokens":100}`
	s, store := ledgerServer(t, body, 0)

	cost := 10.0
	store.Add(usage.Event{
		AccountID: "acct", Model: "m1", TokensKnown: true, Costable: true, CostUSD: &cost,
	})
	ceilingOn(t, s, cost*0.5)

	postChatRefused(t, s)

	for _, e := range store.Recent() {
		if strings.Contains(e.Error, "carried no published price") {
			t.Fatalf("a refusal was blamed on missing prices, which are configured: %s", e.Error)
		}
	}
	panel := spendPanel(t, s)
	if priced, total := panel["pricedCallsLast30Days"], panel["totalCallsLast30Days"]; priced != total {
		t.Fatalf("priced %v of %v calls: the refusal entered the spend window", priced, total)
	}
}

// The same trap exists on the other side of routing: a ceiling that is met while
// a request is being held for spend, rather than while it is being routed.
//
// The two refusals are made in different places and read differently on the wire,
// so a guard applied to only one of them leaves the other poisoning the window.
func TestAHoldRefusalIsNotCountedAsUnmeasuredSpend(t *testing.T) {
	s, store := ledgerServer(t, `{"prompt_tokens":1000,"completion_tokens":100}`, 0)

	// No recorded spend at all, so routing has nothing to object to and the
	// refusal has to come from the hold taken for this request.
	ceilingOn(t, s, 0.0000001)

	postChatRefused(t, s)

	events := store.Recent()
	if len(events) == 0 {
		t.Fatal("the refusal was not recorded")
	}
	if !events[0].NotDispatched {
		t.Fatalf("a hold refusal was not marked as never dispatched: %+v", events[0])
	}
	panel := spendPanel(t, s)
	if panel["spendMeasurable"] != true {
		t.Fatalf("a hold refusal made the window unmeasurable: %+v", panel)
	}
}
