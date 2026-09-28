package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
)

// TestConformanceResponsesToolArgumentsStayAssociatedWithTheirCall catches a
// regression where translating a Responses stream assigned later argument
// deltas to the wrong function call.
func TestConformanceResponsesToolArgumentsStayAssociatedWithTheirCall(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "model"}}})
		case "/v1/chat/completions":
			if !jsonxHasPath(t, r.Body, "tools") {
				t.Fatal("Responses tools were not forwarded to chat upstream")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"id":"chat","model":"model","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"first","arguments":"{\"a\":"}}]}}]}`,
				`data: {"id":"chat","model":"model","choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"second","arguments":"{\"b\":"}}]}}]}`,
				`data: {"id":"chat","model":"model","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}},{"index":1,"function":{"arguments":"2}"}}]}}]}`,
				"data: [DONE]",
				"",
			}, "\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	s := conformanceServer(t, upstream.URL)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","stream":true,"tools":[{"type":"function","name":"first","parameters":{"type":"object"}},{"type":"function","name":"second","parameters":{"type":"object"}}],"input":"go"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	got := rr.Body.String()
	for _, want := range []string{
		`"call_id":"call_a"`, `"arguments":"{\"a\":1}"`,
		`"call_id":"call_b"`, `"arguments":"{\"b\":2}"`,
		"event: response.completed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in stream:\n%s", want, got)
		}
	}
}

// TestConformanceClaudeStreamUsesClaudeLifecycleEvents catches the translation
// regression that wraps an OpenAI stream in a single synthetic Claude event.
func TestConformanceClaudeStreamUsesClaudeLifecycleEvents(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"llama3.2","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("content type %q", got)
	}
	for _, want := range []string{"event: message_start", "event: content_block_delta", "event: message_stop"} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("missing %s in stream:\n%s", want, rr.Body.String())
		}
	}
}

// TestConformanceRouteAliasIsClientVisibleButUpstreamReceivesItsTarget catches
// aliases leaking into an upstream request or an upstream model leaking back to
// a harness that selected the stable route name.
func TestConformanceRouteAliasIsClientVisibleButUpstreamReceivesItsTarget(t *testing.T) {
	var upstreamModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "provider-model"}}})
		case "/v1/chat/completions":
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			upstreamModel = body.Model
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "chat", "model": body.Model,
				"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Routes:        map[string]string{"coding": "provider-model"},
		Providers: []config.Provider{{
			ID: "local", Adapter: "openai_compat", Tier: "local", BaseURL: upstream.URL + "/v1",
		}},
	}
	gw, err := gateway.New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	s := New(Options{Gateway: gw})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"coding","messages":[{"role":"user","content":"hi"}]}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if upstreamModel != "provider-model" {
		t.Fatalf("upstream model = %q, want provider-model", upstreamModel)
	}
	var response struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Model != "coding" {
		t.Fatalf("client model = %q, want coding", response.Model)
	}
}

func conformanceServer(t *testing.T, baseURL string) *Server {
	t.Helper()
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Providers: []config.Provider{{
			ID: "local", Adapter: "openai_compat", Tier: "local", BaseURL: baseURL + "/v1",
		}},
	}
	gw, err := gateway.New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	return New(Options{Gateway: gw})
}

func jsonxHasPath(t *testing.T, body io.Reader, key string) bool {
	t.Helper()
	var value map[string]json.RawMessage
	if err := json.NewDecoder(body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	_, ok := value[key]
	return ok
}
