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

// A streamed call that publishes usage and a ledger that records none is the
// failure this file exists to prevent. It is the shape a spend ceiling refuses
// on: the call is in the window, priced at nothing, and the window reads as
// unmeasurable.
//
// Measured on live accounts 2026-10-11 before the fix, same request shape:
//
//	account            non-stream   stream
//	anthropic-oauth    22/16        none
//	openai-oauth       none         none
//	google-oauth       none         none
//	google-key         2/10         none
//	opencode-zen       none         428/16
//	workers-ai         12/10        12/10
//
// The two causes were that the OpenAI-chat translators never emitted a usage
// object, and that openai_compat never asked for one. Both are covered here
// through the public surface rather than at the adapter, so the test fails if
// either half regresses.

// usageGatedUpstream behaves like the providers that only send usage in a
// stream when the request asks: no stream_options.include_usage, no usage frame.
func usageGatedUpstream(t *testing.T, sawIncludeUsage *bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list", "data": []map[string]string{{"id": "tiny-1b"}},
			})
		case "/v1/chat/completions":
			var body struct {
				Stream        bool `json:"stream"`
				StreamOptions *struct {
					IncludeUsage bool `json:"include_usage"`
				} `json:"stream_options"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.StreamOptions != nil {
				*sawIncludeUsage = body.StreamOptions.IncludeUsage
			}
			if body.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
				if body.StreamOptions != nil && body.StreamOptions.IncludeUsage {
					_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":31,\"completion_tokens\":9,\"total_tokens\":40}}\n\n"))
				}
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "c", "object": "chat.completion", "model": "tiny-1b",
				"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": "hi"}, "finish_reason": "stop"}},
				"usage":   map[string]int{"prompt_tokens": 31, "completion_tokens": 9, "total_tokens": 40},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func usageServer(t *testing.T, baseURL string) (*Server, *usage.Store) {
	t.Helper()
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Providers: []config.Provider{{
			ID: "acct", Adapter: "openai_compat", Tier: "freemium", BaseURL: baseURL,
		}},
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

func postStream(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"tiny-1b","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.RemoteAddr = "127.0.0.1:4321"
	req.Host = "127.0.0.1:8317"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	return rr
}

// The whole path, end to end: PeaProxy asks the upstream for usage, the upstream
// sends it, and the number a user would read in the usage panel is the number
// the provider published.
func TestStreamedCallRecordsUsageFromTheUpstream(t *testing.T) {
	var asked bool
	s, store := usageServer(t, usageGatedUpstream(t, &asked).URL+"/v1")

	rr := postStream(t, s)

	if !asked {
		t.Fatal("PeaProxy streamed without asking for usage, so a provider that gates it sends nothing and the call records no tokens")
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"prompt_tokens":31`) {
		t.Fatalf("the usage frame never reached the client: %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream did not end with [DONE]: %s", body)
	}

	events := store.Recent()
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}
	e := events[0]
	if !e.Stream {
		t.Error("a streamed call was recorded as non-stream")
	}
	if !e.TokensKnown {
		t.Fatal("the ledger recorded a streamed call with unknown tokens: a ceiling over it fails closed forever")
	}
	if e.PromptTokens != 31 || e.CompletionTokens != 9 {
		t.Fatalf("tokens = %d/%d, want 31/9", e.PromptTokens, e.CompletionTokens)
	}
	if !e.Costable {
		t.Error("Costable is false for a call that published both halves of its usage")
	}
	if e.StreamTerminal != TerminalDone {
		t.Errorf("StreamTerminal = %q, want %q", e.StreamTerminal, TerminalDone)
	}
}

// A provider that ignores the request and sends no usage must leave the call
// unmeasured rather than measured at zero. Those are different facts and the
// ledger has to be able to tell them apart.
func TestStreamedCallWithNoUsageUpstreamStaysUnmeasured(t *testing.T) {
	var asked bool
	s, store := usageServer(t, usageGatedUpstream(t, &asked).URL+"/v1")

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"tiny-1b","stream":true,"stream_options":{"include_usage":false},"messages":[{"role":"user","content":"hi"}]}`))
	req.RemoteAddr = "127.0.0.1:4321"
	req.Host = "127.0.0.1:8317"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}

	e := store.Recent()[0]
	if e.TokensKnown {
		t.Fatalf("an upstream that published no usage was recorded as %d/%d tokens", e.PromptTokens, e.CompletionTokens)
	}
	if e.Costable {
		t.Error("Costable is true for a call with no published usage")
	}
}
