package anthropic

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
