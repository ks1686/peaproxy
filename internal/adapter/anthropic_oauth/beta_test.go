package anthropic_oauth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/ks1686/peaproxy/internal/requestmeta"
)

func TestMessagesForwardsClientBetas(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("anthropic-beta"))
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"type":"message","content":[]}`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "oauth-at", ExpiresAt: time.Now().Add(time.Hour)}
	client := "thinking-binding-controls-2026-08-01,oauth-2025-04-20,context-1m-2025-08-07,structured-outputs-2025-11-13"
	ctx := requestmeta.WithRequest(context.Background(), requestmeta.Request{AnthropicBeta: client})
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)

	if _, err := a.Messages(ctx, body); err != nil {
		t.Fatal(err)
	}
	if err := a.MessagesStream(ctx, body, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(got))
	}
	want := oauthBetas("claude-opus-5-5", body) + ",thinking-binding-controls-2026-08-01,structured-outputs-2025-11-13"
	for i, h := range got {
		if h != want {
			t.Errorf("request %d anthropic-beta = %q, want %q", i, h, want)
		}
	}
}
