package gateway

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/config"
)

type streamingResponsesAdapter struct {
	*nativeResponsesAdapter
	streams int
}

func (a *streamingResponsesAdapter) ResponsesStream(_ context.Context, _ []byte, w io.Writer) error {
	a.streams++
	_, err := io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\""+a.responseID+"\"}}\n\n"+
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\""+a.responseID+"\",\"status\":\"completed\"}}\n\n")
	return err
}

// A streamed follow-up carrying previous_response_id must go back to the
// account that holds the response; only the non-stream path honoured it.
func TestStreamedResponsesContinuationStaysOnOriginatingAccount(t *testing.T) {
	first := &streamingResponsesAdapter{nativeResponsesAdapter: &nativeResponsesAdapter{id: "first", model: "model", responseID: "resp_first"}}
	second := &streamingResponsesAdapter{nativeResponsesAdapter: &nativeResponsesAdapter{id: "second", model: "model", responseID: "resp_second"}}
	registry := adapter.NewRegistry()
	registry.Register("native", func(opts adapter.Options) (adapter.Adapter, error) {
		if opts.ID == "first" {
			return first, nil
		}
		return second, nil
	})
	gw, err := New(config.Config{SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317, Providers: []config.Provider{
		{ID: "first", Adapter: "native", Tier: "paid"},
		{ID: "second", Adapter: "native", Tier: "paid"},
	}}, "", registry)
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	var out bytes.Buffer
	if call, err := gw.ResponsesStream(context.Background(), []byte(`{"model":"model","stream":true,"input":"hello"}`), &out); err != nil || call.Account != "first" {
		t.Fatalf("initial stream account = %q, error = %v", call.Account, err)
	}
	out.Reset()
	call, err := gw.ResponsesStream(context.Background(), []byte(`{"model":"model","stream":true,"previous_response_id":"resp_first","input":"continue"}`), &out)
	if err != nil || call.Account != "first" {
		t.Fatalf("continuation stream went to %q (error %v), want the originating account", call.Account, err)
	}
}
