package anthropic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/requestmeta"
)

func TestMessagesForwardsClientBetas(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("anthropic-beta"))
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"type":"message","content":[]}`)
	}))
	t.Cleanup(srv.Close)
	adp, err := New(adapter.Options{ID: "anthropic", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	a := adp.(*Adapter)
	ctx := requestmeta.WithRequest(context.Background(), requestmeta.Request{AnthropicBeta: "thinking-binding-controls-2026-08-01"})
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":8,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hi"}]}`)

	if _, err := a.Messages(ctx, body); err != nil {
		t.Fatal(err)
	}
	if err := a.MessagesStream(ctx, body, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want := "interleaved-thinking-2025-05-14,thinking-binding-controls-2026-08-01"
	for i, h := range got {
		if h != want {
			t.Errorf("request %d anthropic-beta = %q, want %q", i, h, want)
		}
	}
	if len(got) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(got))
	}
}

func TestMessagesForwardsClientBetasWithoutThinkingBudget(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("anthropic-beta")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"type":"message","content":[]}`)
	}))
	t.Cleanup(srv.Close)
	adp, err := New(adapter.Options{ID: "anthropic", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := requestmeta.WithRequest(context.Background(), requestmeta.Request{AnthropicBeta: "oauth-2025-04-20,thinking-binding-controls-2026-08-01"})
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":8,"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hi"}]}`)

	if _, err := adp.(*Adapter).Messages(ctx, body); err != nil {
		t.Fatal(err)
	}
	if got != "thinking-binding-controls-2026-08-01" {
		t.Fatalf("anthropic-beta = %q, want only the non-OAuth client beta", got)
	}
}

func TestAdapterOperationsAndErrors(t *testing.T) {
	var gotPath, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("x-api-key")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-test","display_name":"Claude Test"}]}`)
		case "/v1/messages":
			_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","content":[{"type":"text","text":"hello"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	adp, err := New(adapter.Options{ID: "account", BaseURL: srv.URL + "/v1", APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	a := adp.(*Adapter)
	if a.ID() != "account" || !a.Capabilities().APIKey || !a.Capabilities().Chat || !a.Capabilities().Tools {
		t.Fatalf("identity/capabilities: %q %#v", a.ID(), a.Capabilities())
	}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "claude-test" || models[0].AccountID != "account" || models[0].Provider != Name {
		t.Fatalf("models: %#v %v", models, err)
	}
	if gotPath != "/v1/models" || gotKey != "key" {
		t.Fatalf("models request: %s key=%q", gotPath, gotKey)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "claude-test", Messages: []adapter.Message{{Role: "user", Content: "hi"}}})
	if err != nil || resp.Content != "hello" || resp.Model != "claude-test" {
		t.Fatalf("chat: %#v %v", resp, err)
	}
	if err := a.Validate(context.Background()); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestListModelsAndMessagesReturnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, strings.Repeat("x", 300))
	}))
	t.Cleanup(srv.Close)
	adp, err := New(adapter.Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	a := adp.(*Adapter)
	if _, err := a.ListModels(context.Background()); err == nil {
		t.Fatal("expected list error")
	} else {
		var httpErr adapter.HTTPError
		if !errors.As(err, &httpErr) || httpErr.Status != http.StatusBadGateway || len(httpErr.Body) != 243 {
			t.Fatalf("list error: %#v (%v)", httpErr, err)
		}
	}
	if _, err := a.Messages(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("expected messages error")
	}
}
