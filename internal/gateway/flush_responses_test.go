package gateway

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
)

// rawStreamAdapter is a native Responses adapter that writes bytes verbatim into
// whatever writer it is handed, and stops mid-token.
//
// Real adapters translate, which means their output rarely looks like the input
// byte for byte -- and a test built on one cannot tell whether the route
// rewriter held the tail or the adapter simply never produced it. This one does
// exactly what the byte level requires, which is the only level this invariant
// lives on.
type rawStreamAdapter struct{}

func (a *rawStreamAdapter) ID() string { return "raw-responses" }

func (a *rawStreamAdapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{Chat: true, Stream: true, ListModels: true}
}

func (a *rawStreamAdapter) ListModels(context.Context) ([]catalog.Model, error) {
	return nil, nil
}

func (a *rawStreamAdapter) Chat(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
	return adapter.ChatResponse{}, nil
}

func (a *rawStreamAdapter) ChatStream(context.Context, adapter.ChatRequest, io.Writer) error {
	return nil
}

func (a *rawStreamAdapter) Validate(context.Context) error { return nil }

func (a *rawStreamAdapter) Responses(context.Context, []byte) ([]byte, error) {
	return nil, nil
}

func (a *rawStreamAdapter) ResponsesStream(_ context.Context, _ []byte, w io.Writer) error {
	// A complete model id first, so the rewriter has something to rewrite, then
	// the start of another one -- which it must hold rather than emit, because
	// it cannot yet know what the id is.
	_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"model\":\"zzupstream-model\"}}\n\n")
	_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","response":{"model":"zz`)
	return nil
}

// The Responses wire builds its own route rewriter, and D1's flush was only
// wired into Chat Completions. A Responses stream that ended inside a model name
// therefore lost its tail on the way to the client, with no error to say so --
// the stream just stopped a little early.
func TestResponsesStreamFlushesHeldRouteBytes(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register("raw-responses", func(adapter.Options) (adapter.Adapter, error) {
		return &rawStreamAdapter{}, nil
	})

	cfg := config.Config{
		SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317,
		Routes:    map[string]string{"friendly": "zzupstream-model"},
		Providers: []config.Provider{{ID: "acct", Adapter: "raw-responses", Tier: "paid"}},
	}
	gw, err := New(cfg, "", reg)
	if err != nil {
		t.Fatal(err)
	}
	gw.models = []catalog.Model{{
		ID: "zzupstream-model", AccountID: "acct", Tier: catalog.TierPaid, Routable: true, Exposed: true,
	}}

	var out strings.Builder
	if _, err := gw.ResponsesStream(context.Background(),
		[]byte(`{"model":"friendly","stream":true,"input":"hi"}`), &out); err != nil {
		t.Fatalf("ResponsesStream: %v", err)
	}

	body := out.String()
	if body == "" {
		t.Fatal("nothing reached the client")
	}
	if !strings.Contains(body, `"model":"friendly"`) {
		t.Fatalf("the completed model id was not rewritten for the client:\n%s", body)
	}
	// The rewriter must still be doing its job on the complete id.
	if strings.Contains(body, "zzupstream-model") {
		t.Fatalf("the upstream model id leaked to the client:\n%s", body)
	}
	// And the held tail must arrive.
	if !strings.HasSuffix(body, `"model":"zz`) {
		t.Fatalf("the bytes the rewriter was holding never reached the client:\n%s", body)
	}
}
