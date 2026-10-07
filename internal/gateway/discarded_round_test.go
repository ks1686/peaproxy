package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/contextstore"
	"github.com/ks1686/peaproxy/internal/usage"
)

// twoRoundGateway answers the first round with a search and the second with a
// final answer, so one round is replaced before the client ever sees it and one
// is actually delivered. That is the only shape in which the two can be told
// apart: a loop that stops after the discarded round, or one that never
// discards anything, cannot show whether the mark is real.
func twoRoundGateway(t *testing.T, usageBody string) (*Gateway, *usage.Store, func() int) {
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
				`{"id":"call_` + strconv.Itoa(n) + `","type":"function","function":{"name":"pea_search",` +
				`"arguments":"{\"query\":\"q\"}"}}]}}],"usage":` + usageBody + `}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}],` +
			`"usage":` + usageBody + `}`))
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

// PeaProxy fetched a round, was billed for it, and then threw it away to make
// room for the next one. The ledger recorded it as an ordinary call, so a
// rollup could not tell work nobody received from work that was answered --
// the retrieval loop's cost was invisible while its bill was still real.
func TestARetrievalRoundNobodyReceivedIsCountedSeparatelyFromTheOneThatWas(t *testing.T) {
	body := `{"prompt_tokens":1000,"completion_tokens":100}`
	gw, store, calls := twoRoundGateway(t, body)

	resp, _, err := gw.Chat(context.Background(), []byte(
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`))
	if err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	if resp.Content == "" {
		t.Fatal("no answer reached the client, so nothing was discarded")
	}
	if n := calls(); n != 2 {
		t.Fatalf("upstream saw %d calls, want 2; the loop shape this test rests on is not the one that ran", n)
	}

	events := store.Recent()
	if len(events) != 1 {
		t.Fatalf("the ledger holds %d rounds, want exactly 1: this layer records the round it threw away, "+
			"and the answer the client received is recorded by the server that returns it. Recording both "+
			"here would double-count the one call a user can actually see (%d held, upstream saw %d)",
			len(events), len(events), calls())
	}
	if !events[0].Discarded {
		t.Fatalf("the recorded round is not marked discarded: %+v. An unmarked round is indistinguishable "+
			"from one the client received, which is the whole thing this mark is for", events[0])
	}

	rows := store.ByAccount()
	if len(rows) != 1 {
		t.Fatalf("the rollup has %d accounts, want 1", len(rows))
	}
	r := rows[0]
	if r.Discarded != 1 {
		t.Fatalf("the rollup counts %d discarded rounds, want 1", r.Discarded)
	}
	if r.Calls != 1 {
		t.Fatalf("the rollup counts %d calls, want 1 -- a discarded round is still a call the user paid for", r.Calls)
	}
	if want := 1100; r.DiscardedTokens != want {
		t.Fatalf("the rollup attributes %d tokens to discarded rounds, want %d", r.DiscardedTokens, want)
	}
	if r.Tokens != 1100 {
		t.Fatalf("the rollup totals %d tokens, want 1100: discarding a round does not make it cheaper", r.Tokens)
	}

	// (1000 * 3.0 + 100 * 15.0) / 1e6 = 0.0045, for exactly the discarded round.
	if r.DiscardedEstimatedUSD == nil {
		t.Fatal("the discarded round was priced but the rollup reports no figure for it")
	}
	if got := *r.DiscardedEstimatedUSD; got < 0.0044999 || got > 0.0045001 {
		t.Fatalf("the rollup puts %v on the discarded round, want 0.0045", got)
	}
	if r.DiscardedEstimatedCalls != 1 {
		t.Fatalf("the rollup counts %d estimated discarded rounds, want 1", r.DiscardedEstimatedCalls)
	}
	if r.DiscardedUSD != nil {
		t.Fatalf("the rollup reports a provider-published figure of %v, but this provider published none: "+
			"an estimate is a reading of a bill, never the bill", *r.DiscardedUSD)
	}
}

// The same shape, with a provider that published only half its usage. The
// round is still a discarded round and still counted as one; what cannot be
// stated is what it cost, and the rollup must say so rather than report the
// part it can price.
func TestARetrievalRoundNobodyCouldPriceLeavesTheFigureUnknownRatherThanZero(t *testing.T) {
	body := `{"prompt_tokens":1000}`
	gw, store, _ := twoRoundGateway(t, body)

	if _, _, err := gw.Chat(context.Background(), []byte(
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`)); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}

	rows := store.ByAccount()
	if len(rows) != 1 {
		t.Fatalf("the rollup has %d accounts, want 1", len(rows))
	}
	r := rows[0]
	if r.Discarded != 1 {
		t.Fatalf("the rollup counts %d discarded rounds, want 1: an unpriced round is still a round", r.Discarded)
	}
	if r.DiscardedTokens != 1000 {
		t.Fatalf("the rollup attributes %d tokens to the discarded round, want 1000", r.DiscardedTokens)
	}
	if r.DiscardedEstimatedUSD != nil {
		t.Fatalf("the rollup states %v for a round nobody could price. Half a usage object is not "+
			"half a cost, and zero here would read as \"retrieval cost nothing\"",
			*r.DiscardedEstimatedUSD)
	}
	if r.DiscardedUSD != nil {
		t.Fatalf("the rollup states a provider-published figure of %v, but this provider published none",
			*r.DiscardedUSD)
	}
}
