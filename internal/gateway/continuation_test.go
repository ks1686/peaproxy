package gateway

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
)

func TestContinuationCannotCrossAccount(t *testing.T) {
	candidates := []instance{
		{Provider: config.Provider{ID: "origin"}},
		{Provider: config.Provider{ID: "other"}},
	}
	got := continuationFirst(candidates, "origin")
	if len(got) != 1 || got[0].Provider.ID != "origin" {
		t.Fatalf("candidates = %#v", got)
	}
}

func TestResponsesContinuationStaysOnOriginatingAccount(t *testing.T) {
	first := &nativeResponsesAdapter{id: "first", model: "model", responseID: "resp_first"}
	second := &nativeResponsesAdapter{id: "second", model: "model", responseID: "resp_second"}
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

	if _, account, err := gw.Responses(context.Background(), []byte(`{"model":"model","input":"hello"}`)); err != nil || account != "first" {
		t.Fatalf("initial account = %q, error = %v", account, err)
	}
	if _, account, err := gw.Responses(context.Background(), []byte(`{"model":"model","previous_response_id":"resp_first","input":"continue"}`)); err != nil || account != "first" {
		t.Fatalf("continuation account = %q, error = %v", account, err)
	}
	if first.calls != 2 || second.calls != 0 {
		t.Fatalf("calls first=%d second=%d, want first=2 second=0", first.calls, second.calls)
	}
}

func TestUnknownResponsesContinuationDoesNotInventAccountAffinity(t *testing.T) {
	first := &nativeResponsesAdapter{id: "first", model: "model", responseID: "resp_first"}
	second := &nativeResponsesAdapter{id: "second", model: "model", responseID: "resp_second"}
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

	_, account, err := gw.Responses(context.Background(), []byte(`{"model":"model","previous_response_id":"resp_unknown","input":"continue"}`))
	if err != nil || account != "first" {
		t.Fatalf("unknown continuation account = %q, error = %v", account, err)
	}
}

func TestContinuationBindingExpiresAndRejectsModelMismatch(t *testing.T) {
	g := &Gateway{continuations: map[string]continuationBind{}}
	g.continuations["resp_old"] = continuationBind{Account: "account", Model: "model", Until: time.Now().Add(-time.Second)}
	if got := g.continuationAccount("resp_old", "model"); got != "" {
		t.Fatalf("expired continuation account = %q", got)
	}
	g.continuations["resp_model"] = continuationBind{Account: "account", Model: "model-a", Until: time.Now().Add(time.Hour)}
	if got := g.continuationAccount("resp_model", "model-b"); got != "" {
		t.Fatalf("mismatched continuation account = %q", got)
	}
}

func TestRemoveProviderInvalidatesContinuationBindings(t *testing.T) {
	g := &Gateway{
		cfg:           config.Config{Providers: []config.Provider{{ID: "gone"}}},
		continuations: map[string]continuationBind{"resp": {Account: "gone", Model: "model", Until: time.Now().Add(time.Hour)}},
	}
	if err := g.RemoveProvider(context.Background(), "gone"); err != nil {
		t.Fatal(err)
	}
	if got := g.continuationAccount("resp", "model"); got != "" {
		t.Fatalf("removed provider continuation account = %q", got)
	}
}

type nativeResponsesAdapter struct {
	id         string
	model      string
	responseID string
	calls      int
}

func (a *nativeResponsesAdapter) ID() string { return a.id }
func (a *nativeResponsesAdapter) ListModels(context.Context) ([]catalog.Model, error) {
	return []catalog.Model{{ID: a.model, AccountID: a.id, Provider: "native", Tier: catalog.TierPaid}}, nil
}
func (a *nativeResponsesAdapter) Chat(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
	return adapter.ChatResponse{}, adapter.ErrNotImplemented
}
func (a *nativeResponsesAdapter) ChatStream(context.Context, adapter.ChatRequest, io.Writer) error {
	return adapter.ErrNotImplemented
}
func (a *nativeResponsesAdapter) Validate(context.Context) error { return nil }
func (a *nativeResponsesAdapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{Chat: true, Stream: true}
}
func (a *nativeResponsesAdapter) Responses(_ context.Context, _ []byte) ([]byte, error) {
	a.calls++
	return json.Marshal(map[string]string{"id": a.responseID, "object": "response", "model": a.model})
}
func (a *nativeResponsesAdapter) ResponsesStream(context.Context, []byte, io.Writer) error {
	return adapter.ErrNotImplemented
}
