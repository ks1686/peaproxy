package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
)

// claudeStreamGateway serves a native Anthropic Messages stream for the upstream
// id "zzupstream-model", reached through the client alias "friendly", and ends
// part-way through that model name.
//
// That end matters: the route rewriter holds bytes that could still turn out to
// be a model name, and a stream that stops there is exactly the case where the
// held bytes are lost if nobody flushes them. The client is the only place they
// can go.
func claudeStreamGateway(t *testing.T, adapterName string) *Gateway {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "zzupstream-model"}}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"zzupstream-model\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
		// The upstream stops in the middle of the model name.
		_, _ = io.WriteString(w, `data: {"type":"message_delta","usage":{"output_tokens":1},"model":"zz`)
	}))
	t.Cleanup(up.Close)

	cfg := config.Config{
		SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317,
		Routes: map[string]string{"friendly": "zzupstream-model"},
		Providers: []config.Provider{{
			ID: "acct", Adapter: adapterName, Tier: "paid", BaseURL: up.URL,
			OAuth: &config.OAuthToken{AccessToken: "t", RefreshToken: "r"},
		}},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	// The oauth adapter publishes its own catalogue, so the fake upstream's
	// model is not in it. The row is what routing resolves against, and the
	// point of this test is the writer at the end of the stream, not how the
	// catalogue was built.
	gw.models = []catalog.Model{{
		ID: "zzupstream-model", AccountID: "acct", Tier: catalog.TierPaid,
		Routable: true, Exposed: true,
	}}
	return gw
}

// The D1 fix flushes the route rewriter on Chat Completions. The Messages wire
// did not, so a stream that ended inside a model name lost those bytes on the
// way out -- the stream stopped slightly earlier than the upstream did, with no
// error anywhere to explain the missing tail.
func TestMessagesStreamFlushesHeldRouteBytes(t *testing.T) {
	gw := claudeStreamGateway(t, "anthropic")

	var out strings.Builder
	_, _ = gw.ClaudeChatStream(context.Background(),
		[]byte(`{"model":"friendly","stream":true,"messages":[{"role":"user","content":"hi"}]}`), &out)

	body := out.String()
	if body == "" {
		t.Fatal("nothing reached the client")
	}
	// The tail the rewriter was holding, and nothing but it.
	if !strings.HasSuffix(body, `"model":"zz`) {
		t.Fatalf("the bytes the rewriter was holding never reached the client:\n%s", body)
	}
	// The rewriter must still be doing its job: the client asked for "friendly"
	// and must never be told it is talking to "zzupstream-model".
	if strings.Contains(body, "zzupstream-model") {
		t.Fatalf("the upstream model id leaked to the client:\n%s", body)
	}
	if !strings.Contains(body, "friendly") {
		t.Fatalf("the client model id was not restored:\n%s", body)
	}
}
