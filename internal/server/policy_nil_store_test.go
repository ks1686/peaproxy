package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ks1686/peaproxy/internal/usage"
)

// The policy panel has to survive a gateway with no ledger attached. Every other
// field in this payload already handled a nil store -- the spend total below it
// is guarded -- and then inFlightReservedUSD dereferenced it unconditionally,
// so SetUsage(nil) turned an admin page into a panic. A 500 on /admin/policy is
// a small failure until it is the only thing telling you what your ceiling is
// doing.
func TestPolicyPanelSurvivesALedgerlessGateway(t *testing.T) {
	srv, _ := ledgerServer(t, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`, 1.0)

	srv.gw.SetUsage(nil)

	req := httptest.NewRequest(http.MethodGet, "/admin/policy", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/admin/policy returned %d with no ledger attached: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Policy map[string]any `json:"policy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got, ok := body.Policy["inFlightReservedUSD"]
	if !ok {
		t.Fatalf("the panel omits inFlightReservedUSD entirely: %v", body.Policy)
	}
	if n, ok := got.(float64); !ok || n != 0 {
		t.Fatalf("inFlightReservedUSD = %v, want 0 when nothing is held", got)
	}
}

// The complement: with a ledger attached and a hold in flight, the panel has to
// report the hold. Reporting zero whenever the store happens to be empty is the
// same code as reporting zero always, and the row exists to show spend that is
// committed but not yet measured.
func TestPolicyPanelReportsAHoldInFlight(t *testing.T) {
	srv, store := ledgerServer(t, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`, 1.0)

	if _, held := store.HoldSpend(30, func(usage.SpendWindow) bool { return true }, 0.25); !held {
		t.Fatal("the test could not take a hold, so it would prove nothing")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/policy", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/admin/policy returned %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Policy struct {
			InFlight float64 `json:"inFlightReservedUSD"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Policy.InFlight != 0.25 {
		t.Fatalf("inFlightReservedUSD = %v, want the 0.25 held", body.Policy.InFlight)
	}
}
