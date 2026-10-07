package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/contextopt"
	"github.com/ks1686/peaproxy/internal/contextstore"
	"github.com/ks1686/peaproxy/internal/usage"
)

// searchForeverGateway asks for another search on every call, so the round budget
// runs out with a round outstanding -- the path that ends in a refusal rather
// than an answer. Every call publishes usage, which is what makes a lost round
// visible in the ledger.
func searchForeverGateway(t *testing.T, usageBody string) (*Gateway, *usage.Store, func() int) {
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
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[` +
			`{"id":"call_` + strconv.Itoa(n) + `","type":"function","function":{"name":"pea_search",` +
			`"arguments":"{\"query\":\"q\"}"}}]}}],"usage":` + usageBody + `}`))
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

// Every round PeaProxy fetches is billed by the provider. The rounds replaced by
// a follow-up were recorded as discarded, but the final one -- the round where the
// budget ran out -- was thrown away with no record at all.
//
// That is the worst round to lose: it is the one the client certainly paid for,
// and it is the round that ends a runaway loop, so nobody would see it going
// missing.
func TestTheRoundThatEndsTheLoopStillReachesTheLedger(t *testing.T) {
	body := `{"prompt_tokens":1000,"completion_tokens":100}`
	gw, store, calls := searchForeverGateway(t, body)

	_, _, err := gw.Chat(context.Background(), []byte(
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`))

	var roundErr RoundLimitError
	if !errors.As(err, &roundErr) {
		t.Fatalf("the turn ended with %v, not a round-limit refusal", err)
	}
	if !strings.Contains(roundErr.Error(), contextopt.ToolName) {
		t.Fatalf("the refusal does not name the loop that ended: %v", err)
	}

	upstream := calls()
	if upstream < 2 {
		t.Fatalf("upstream saw %d calls; the loop never ran, so this proves nothing", upstream)
	}

	// No answer reached the client -- the turn was refused -- so every upstream
	// call was fetched and discarded, and every one of them has to be in the
	// ledger.
	events := store.Recent()
	if len(events) != upstream {
		t.Fatalf("the ledger holds %d discarded rounds, want %d: upstream was called %d times, "+
			"nothing was answered to the client, and every one of those calls was billed",
			len(events), upstream, upstream)
	}
	for _, e := range events {
		if !e.TokensKnown || !e.Costable {
			t.Fatalf("a discarded round was recorded without its usage: %+v", e)
		}
	}
}
