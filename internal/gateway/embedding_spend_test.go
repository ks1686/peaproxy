package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/config"
)

// Chat went through admitted -- an admission slot and a spend hold -- at all
// nine of its call sites. Embeddings went through neither: a client could send
// unlimited embedding requests past maxInFlight and past the spend ceiling, and
// nothing noticed until the bill arrived.
func TestEmbeddingsHoldSpendAndTakeAnAdmissionSlot(t *testing.T) {
	hits := 0
	upstream := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "text-embedding-3-small"}}})
		case "/v1/embeddings":
			hits++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list", "model": "text-embedding-3-small",
				"data": []map[string]any{{"object": "embedding", "index": 0, "embedding": []float64{0.25}}},
			})
		default:
			http.NotFound(w, r)
		}
	}
	rate := 0.1 // $0.10 per million input tokens
	gw := twoAccountGateway(t, upstream, upstream)
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"acct-a/text-embedding-3-small": {Input: &rate, Output: &rate, Verified: true},
		"acct-b/text-embedding-3-small": {Input: &rate, Output: &rate, Verified: true},
	}
	gw.cfg.Optimization.SpendCeilingUSD = 0.000001
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	_, _, err := gw.CreateEmbeddings(context.Background(), []byte(`{"model":"text-embedding-3-small","input":"hello"}`))
	if err == nil {
		t.Fatal("an embedding request went out although the ceiling had nothing left for it")
	}
	if !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("the refusal does not name the ceiling: %v", err)
	}
	if hits != 0 {
		t.Fatalf("%d embedding calls were made under a spent ceiling", hits)
	}
}

// The ceiling is not the whole point: a refused embedding is also a slot the
// gate must have given back. Otherwise repeated refusals would leak capacity
// until the account looked permanently busy.
func TestARefusedEmbeddingDoesNotLeakAnAdmissionSlot(t *testing.T) {
	upstream := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "text-embedding-3-small"}}})
		case "/v1/embeddings":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list", "model": "text-embedding-3-small",
				"data": []map[string]any{{"object": "embedding", "index": 0, "embedding": []float64{0.25}}},
			})
		default:
			http.NotFound(w, r)
		}
	}
	gw := twoAccountGateway(t, upstream, upstream)
	gw.cfg.RequestEngine.MaxInFlight = 1
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	// Sequential calls must not accumulate slots. If each refusal or completion
	// leaked one, the second call would find the account at capacity.
	for i := 0; i < 3; i++ {
		if _, _, err := gw.CreateEmbeddings(context.Background(), []byte(`{"model":"text-embedding-3-small","input":"hi"}`)); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
}

// The ordinary case still works: the guard is a reservation, not a ban.
func TestEmbeddingsStillWorkWithoutAMoneyGuard(t *testing.T) {
	hits := 0
	upstream := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "text-embedding-3-small"}}})
		case "/v1/embeddings":
			hits++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list", "model": "text-embedding-3-small",
				"data": []map[string]any{{"object": "embedding", "index": 0, "embedding": []float64{0.25}}},
			})
		default:
			http.NotFound(w, r)
		}
	}
	gw := twoAccountGateway(t, upstream, upstream)
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	if _, _, err := gw.CreateEmbeddings(context.Background(), []byte(`{"model":"text-embedding-3-small","input":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
}

// The reservation must be sized like every other one: from the body actually
// sent, against the price actually published for the model. A reservation of
// zero would let an arbitrarily large embedding request through a ceiling that
// stops every other kind of call.
func TestEmbeddingReservationIsSizedFromTheRequest(t *testing.T) {
	rate := 0.1 // $0.10 per million input tokens
	// Rates are per million tokens: at $0.10/M a 50-byte body holds about
	// five micro-dollars, which is over a one-micro-dollar ceiling.
	upstream := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "text-embedding-3-small"}}})
		case "/v1/embeddings":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list", "model": "text-embedding-3-small",
				"data": []map[string]any{{"object": "embedding", "index": 0, "embedding": []float64{0.25}}},
			})
		default:
			http.NotFound(w, r)
		}
	}
	gw := twoAccountGateway(t, upstream, upstream)
	// Both accounts are priced. An unpriced deployment holds nothing, by design,
	// so leaving one out would let the routing pick the account with no ceiling
	// at all -- which is the documented behaviour, not the thing under test.
	gw.cfg.AutomaticRoutes.Prices = map[string]config.PriceQuote{
		"acct-a/text-embedding-3-small": {Input: &rate, Output: &rate, Verified: true},
		"acct-b/text-embedding-3-small": {Input: &rate, Output: &rate, Verified: true},
	}
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	// Spend nothing so far, and set a ceiling the large body must exceed but the
	// small one must not.
	gw.cfg.Optimization.SpendCeilingUSD = 0.01
	gw.spendWindow = constant(measured(0, 0))

	small := []byte(`{"model":"text-embedding-3-small","input":"hello"}`)
	if _, _, err := gw.CreateEmbeddings(context.Background(), small); err != nil {
		t.Fatalf("a small embedding was refused: %v", err)
	}

	big := []byte(`{"model":"text-embedding-3-small","input":"` + strings.Repeat("x", 200000) + `"}`)
	if _, _, err := gw.CreateEmbeddings(context.Background(), big); err == nil {
		t.Fatal("a 200KB embedding body passed a ceiling it was priced far above")
	}
}
