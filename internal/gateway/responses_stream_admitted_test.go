package gateway

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/usage"
)

// The native Responses *streaming* branch used to call the provider directly,
// while its non-streaming sibling went through admitted. So maxInFlight and the
// spend reservation were enforced on every attempt except this one -- and the
// invariant written into the code said "every upstream attempt".
func TestNativeResponsesStreamHoldsSpendAndTakesASlot(t *testing.T) {
	hits := 0
	probe := &countingResponsesAdapter{onCall: func() { hits++ }}

	reg := adapter.NewRegistry()
	reg.Register("counting-responses", func(adapter.Options) (adapter.Adapter, error) {
		return probe, nil
	})

	rate := 0.1 // $0.10 per million input tokens
	cfg := config.Config{
		SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317,
		AutomaticRoutes: config.AutomaticRoutePrefs{
			Enabled: true,
			Prices: map[string]config.PriceQuote{
				"acct/upstream": {Input: &rate, Output: &rate, Verified: true},
			},
		},
		Providers: []config.Provider{{ID: "acct", Adapter: "counting-responses", Tier: "paid", APIKey: "sk-test"}},
	}
	gw, err := New(cfg, "", reg)
	if err != nil {
		t.Fatal(err)
	}
	gw.SetUsage(usage.Open(""))
	gw.Refresh(context.Background())

	// A ceiling the streaming request's input is priced far above. If the branch
	// takes no hold, the call goes upstream and the ceiling is never consulted.
	gw.cfg.Optimization.SpendCeilingUSD = 0.000001
	_ = gw.rebuild()

	var out bytes.Buffer
	body := []byte(`{"model":"upstream","stream":true,"input":"a prompt long enough that its reservation exceeds a one-micro-dollar ceiling"}`)
	_, err = gw.ResponsesStream(context.Background(), body, &out)
	if err == nil {
		t.Fatal("a native Responses stream went upstream although the ceiling had nothing left for it")
	}
	if !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("the refusal does not name the ceiling: %v", err)
	}
	if hits != 0 {
		t.Fatalf("%d streaming calls were made under a spent ceiling", hits)
	}
}

// countingResponsesAdapter is a native Responses adapter that records calls and
// streams a minimal valid frame.
type countingResponsesAdapter struct{ onCall func() }

func (a *countingResponsesAdapter) ID() string { return "counting-responses" }

func (a *countingResponsesAdapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{Chat: true, Stream: true, ListModels: true, APIKey: true}
}

func (a *countingResponsesAdapter) ListModels(context.Context) ([]catalog.Model, error) {
	// One model, so the request has something to route to. An adapter that lists
	// nothing cannot be exercised for a guard that runs after selection.
	return []catalog.Model{{ID: "upstream", AccountID: "acct", Tier: catalog.TierPaid}}, nil
}

func (a *countingResponsesAdapter) Chat(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
	return adapter.ChatResponse{}, nil
}

func (a *countingResponsesAdapter) ChatStream(context.Context, adapter.ChatRequest, io.Writer) error {
	return nil
}

func (a *countingResponsesAdapter) Validate(context.Context) error { return nil }

func (a *countingResponsesAdapter) Responses(context.Context, []byte) ([]byte, error) {
	if a.onCall != nil {
		a.onCall()
	}
	return []byte(`{"usage":{"input_tokens":10,"output_tokens":5}}`), nil
}

func (a *countingResponsesAdapter) ResponsesStream(_ context.Context, _ []byte, w io.Writer) error {
	if a.onCall != nil {
		a.onCall()
	}
	_, err := io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"upstream\"}}\n\n")
	return err
}
