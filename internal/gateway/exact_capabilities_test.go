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
	"github.com/ks1686/peaproxy/internal/requestmeta"
)

// Naming a model must not be a way around the capabilities an endpoint declared.
//
// A provider that says `tools: false` is describing an endpoint that accepts a
// tools array and ignores it. Routing a tool-calling request there loses the
// call, and the client is told it succeeded -- which is the same loss as sending
// it nowhere, with worse diagnostics.
func TestAnExactModelIsNotRoutedToADeploymentThatCannotRunItsTools(t *testing.T) {
	gw, upstream := toollessGateway(t)

	_, _, err := gw.Chat(requestmeta.WithRequest(context.Background(), requestmeta.Request{
		Wire:         requestmeta.WireChat,
		Requirements: requestmeta.RequirementsFromBody(requestmeta.WireChat, []byte(withToolsBody)),
	}), []byte(withToolsBody))

	if err == nil {
		t.Fatal("a tools request was served by a deployment declared tools: false")
	}
	if upstream() != 0 {
		t.Fatalf("the request reached the endpoint anyway (%d calls)", upstream())
	}
	if !strings.Contains(err.Error(), "tools") {
		t.Fatalf("the refusal does not name what was missing: %v", err)
	}
	// The refusal has to say what to do about it, or it is just a dead end.
	if !strings.Contains(err.Error(), "capabilities") {
		t.Fatalf("the refusal names no way out: %v", err)
	}
}

// The guard filters rather than refuses outright: one account that cannot run
// tools must not stop a request another account could have answered.
func TestAnExactModelPrefersADeploymentThatCanRunItsTools(t *testing.T) {
	gw, upstream := mixedToolGateway(t)

	if _, _, err := gw.Chat(requestmeta.WithRequest(context.Background(), requestmeta.Request{
		Wire:         requestmeta.WireChat,
		Requirements: requestmeta.RequirementsFromBody(requestmeta.WireChat, []byte(withToolsBody)),
	}), []byte(withToolsBody)); err != nil {
		t.Fatalf("a tools request was refused even though one account could serve it: %v", err)
	}
	if upstream() != "capable" {
		t.Fatalf("the tools request went to %q, want the account that can run tools", upstream())
	}
}

const withToolsBody = `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],` +
	`"tools":[{"type":"function","function":{"name":"lookup"}}]}`

// toollessGateway is one account whose endpoint accepts a tools array and ignores
// it, declared as such rather than guessed.
func toollessGateway(t *testing.T) (*Gateway, func() int) {
	t.Helper()
	seen := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "gpt-5"}}})
			return
		}
		seen = "hit"
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	t.Cleanup(srv.Close)

	tools := false
	cfg := config.Config{
		SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317,
		Providers: []config.Provider{{
			ID: "toolless", Adapter: "openai_compat", Tier: "paid", BaseURL: srv.URL + "/v1",
			APIKey:       "sk-test",
			Capabilities: config.ProviderCapabilities{Tools: &tools},
		}},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	return gw, func() int {
		if seen == "" {
			return 0
		}
		return 1
	}
}

// mixedToolGateway is two accounts for the same model, one of which can run
// tools. A filter that refuses on the first blocked candidate would break this.
func mixedToolGateway(t *testing.T) (*Gateway, func() string) {
	t.Helper()
	var mu sync.Mutex
	answered := ""
	srvFor := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "gpt-5"}}})
				return
			}
			mu.Lock()
			answered = name
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
				"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
			})
		}))
	}
	toolless := srvFor("toolless")
	capable := srvFor("capable")
	t.Cleanup(toolless.Close)
	t.Cleanup(capable.Close)

	no, yes := false, true
	cfg := config.Config{
		SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317,
		Providers: []config.Provider{
			{ID: "a-toolless", Adapter: "openai_compat", Tier: "paid", BaseURL: toolless.URL + "/v1",
				APIKey: "sk-test", Capabilities: config.ProviderCapabilities{Tools: &no}},
			{ID: "b-capable", Adapter: "openai_compat", Tier: "paid", BaseURL: capable.URL + "/v1",
				APIKey: "sk-test", Capabilities: config.ProviderCapabilities{Tools: &yes}},
		},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	return gw, func() string {
		mu.Lock()
		defer mu.Unlock()
		return answered
	}
}
