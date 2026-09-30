package anthropic_oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/oauth"
)

// cutStreamServer sends half a tool_use turn as a chunked 200, then drops the
// connection without the terminating chunk.
func cutStreamServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-5-5\"}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"write\"}}\n\n"
		fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", len(body), body)
		_ = buf.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestChatStreamCutUpstreamNeverFinishesTheTurn(t *testing.T) {
	a := testAdapter(t, cutStreamServer(t).URL)
	a.token = oauth.Token{AccessToken: "oauth-at", ExpiresAt: time.Now().Add(time.Hour)}
	var out strings.Builder
	req := adapter.ChatRequest{Model: "claude-opus-5-5", Raw: []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)}

	err := a.ChatStream(context.Background(), req, &out)

	if err == nil {
		t.Fatal("cut upstream returned no error")
	}
	for _, deny := range []string{`"finish_reason":"`, "data: [DONE]"} {
		if strings.Contains(out.String(), deny) {
			t.Fatalf("cut upstream reached the client as a finished turn (%s):\n%s", deny, out.String())
		}
	}
}
