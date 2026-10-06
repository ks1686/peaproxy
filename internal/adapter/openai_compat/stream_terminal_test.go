package openai_compat

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
)

// A complete upstream stream must arrive complete, however the network split
// it. This is the wire family the free-tier providers use, so a loss here is
// indistinguishable at the client from the provider having stopped early.
func TestChatStreamDeliversTheTerminalEventAtEverySplitPoint(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		"",
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		"",
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		"data: [DONE]",
		"",
		"",
	}, "\n")

	for split := 0; split <= len(upstream); split++ {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			_, _ = io.WriteString(w, upstream[:split])
			if fl != nil {
				fl.Flush()
			}
			_, _ = io.WriteString(w, upstream[split:])
		}))
		a, err := New(adapter.Options{ID: "test", BaseURL: srv.URL, APIKey: "k"})
		if err != nil {
			t.Fatal(err)
		}

		var out bytes.Buffer
		err = a.ChatStream(context.Background(), adapter.ChatRequest{
			Model: "m",
			Raw:   []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
		}, &out)
		srv.Close()
		if err != nil {
			t.Fatalf("split %d: stream errored: %v", split, err)
		}

		got := out.String()
		if !strings.Contains(got, `"finish_reason":"stop"`) {
			t.Fatalf("split %d: the terminal finish_reason was lost", split)
		}
		if !strings.Contains(got, "[DONE]") {
			t.Fatalf("split %d: the [DONE] sentinel was lost", split)
		}
		if !strings.Contains(got, "Hello") {
			t.Fatalf("split %d: content was lost", split)
		}
	}
}

// The mirror image: when the provider genuinely stops early, the bytes that did
// arrive must still reach the client. Swallowing them would make a truncated
// stream look like an empty one, and would hide how far it actually got.
func TestChatStreamKeepsBytesFromATruncatedUpstream(t *testing.T) {
	// No finish_reason, no [DONE]: the provider stopped.
	upstream := `data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"partial ans"}}]}` + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstream)
	}))
	defer srv.Close()

	a2, err := New(adapter.Options{ID: "test", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_ = a2.ChatStream(context.Background(), adapter.ChatRequest{
		Model: "m",
		Raw:   []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, &out)

	if !strings.Contains(out.String(), "partial ans") {
		t.Fatalf("a truncated upstream was swallowed entirely: %q", out.String())
	}
	if strings.Contains(out.String(), "[DONE]") {
		t.Fatal("a terminal sentinel was fabricated for a stream that never sent one")
	}
}
