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
	"github.com/ks1686/peaproxy/internal/contextopt"
	"github.com/ks1686/peaproxy/internal/contextstore"
)

// searchingForever serves a proxy tool call for every request, so the round
// budget is the only thing that can end the loop.
func searchingForever(t *testing.T) (*Gateway, func() int) {
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
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[` +
			`{"id":"call_x","type":"function","function":{"name":"pea_search","arguments":"{\"query\":\"q\"}"}}]}}]}`))
	}))
	t.Cleanup(up.Close)

	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Providers: []config.Provider{{
			ID: "acct", Adapter: "openai_compat", Tier: "paid", BaseURL: up.URL + "/v1",
		}},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Artifacts = contextstore.New(contextstore.Options{})
	gw.cfg.Optimization.Automatic = boolp(true)
	gw.Refresh(context.Background())
	return gw, func() int { mu.Lock(); defer mu.Unlock(); return calls }
}

// When the budget runs out, PeaProxy's own tool call must not be handed to the
// client as if the client had made it. The client never declared a pea_search,
// has no handler for it, and will either fail on the unknown tool or show the
// user a search it did not ask for.
func TestRoundLimitDoesNotLeakTheProxyToolCall(t *testing.T) {
	gw, calls := searchingForever(t)

	_, _, err := gw.Chat(context.Background(), []byte(
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`))
	if err == nil {
		t.Fatal("an exhausted search budget returned a normal answer")
	}
	if !strings.Contains(err.Error(), contextopt.ToolName) {
		t.Fatalf("the refusal does not say which loop ended: %v", err)
	}
	// The budget bounds PeaProxy's own rounds; the call that first asked for one
	// is not a round, so the upstream sees at most one more than the budget.
	if got := calls(); got > contextopt.MaxRounds+1 {
		t.Fatalf("the upstream saw %d calls, past the bound of %d", got, contextopt.MaxRounds+1)
	}
}

// The exhaustion must not be silent either: a request that ends early has to say
// so, because a truncated answer that looks complete is the failure mode this
// project keeps running into.
func TestRoundLimitRefusalExplainsItself(t *testing.T) {
	gw, _ := searchingForever(t)

	_, _, err := gw.Chat(context.Background(), []byte(
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "pea_search") || !strings.Contains(strings.ToLower(err.Error()), "limit") {
		t.Fatalf("the refusal is not specific about the round limit: %v", err)
	}
}

// A search that finishes inside the budget must be unaffected: this is a guard
// on the refusal path, not a reason to stop answering early.
func TestASearchInsideTheBudgetStillAnswers(t *testing.T) {
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
				`{"id":"call_1","type":"function","function":{"name":"pea_search","arguments":"{\"query\":\"q\"}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"the answer"}}]}`))
	}))
	t.Cleanup(up.Close)

	cfg := config.Config{
		SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317,
		Providers: []config.Provider{{ID: "acct", Adapter: "openai_compat", Tier: "paid", BaseURL: up.URL + "/v1"}},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Artifacts = contextstore.New(contextstore.Options{})
	gw.cfg.Optimization.Automatic = boolp(true)
	gw.Refresh(context.Background())

	resp, _, err := gw.Chat(context.Background(), []byte(
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`))
	if err != nil {
		t.Fatalf("a search inside the budget failed: %v", err)
	}
	if !strings.Contains(string(resp.Raw), "the answer") {
		t.Fatalf("the answer was not returned: %s", resp.Raw)
	}
}
