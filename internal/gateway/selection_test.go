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

func TestChatReturnsDeploymentOfTheAttempt(t *testing.T) {
	var gotModel string
	recording := &recordingAdapter{chat: func(_ context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
		gotModel = req.Model
		return adapter.ChatResponse{Model: req.Model, Raw: []byte(`{"model":"gemini-3.8-flash-high","choices":[{"message":{"content":"hi"}}]}`)}, nil
	}}
	registry := adapter.NewRegistry()
	registry.Register("recording", func(adapter.Options) (adapter.Adapter, error) { return recording, nil })
	gw, err := New(config.Config{Providers: []config.Provider{{ID: "account", Adapter: "recording"}}}, "", registry)
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())

	resp, call, err := gw.Chat(context.Background(), []byte(`{"model":"gemini-3.8-flash","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotModel != "gemini-3.8-flash-high" || call.Deployment != gotModel || call.Account != "account" {
		t.Fatalf("upstream=%q call=%+v", gotModel, call)
	}
	if resp.Model != "gemini-3.8-flash" || !strings.Contains(string(resp.Raw), `"model":"gemini-3.8-flash"`) {
		t.Fatalf("response did not retain client model: %+v", resp)
	}
}

type recordingAdapter struct {
	chat func(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error)
}

func (a *recordingAdapter) ID() string { return "recording" }
func (a *recordingAdapter) ListModels(context.Context) ([]catalog.Model, error) {
	return []catalog.Model{
		{ID: "gemini-3.8-flash-low", AccountID: "account", Provider: "recording", Tier: catalog.TierPaid, Exposed: true, Routable: true},
		{ID: "gemini-3.8-flash-medium", AccountID: "account", Provider: "recording", Tier: catalog.TierPaid, Exposed: true, Routable: true},
		{ID: "gemini-3.8-flash-high", AccountID: "account", Provider: "recording", Tier: catalog.TierPaid, Exposed: true, Routable: true},
	}, nil
}
func (a *recordingAdapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	return a.chat(ctx, req)
}
func (*recordingAdapter) ChatStream(context.Context, adapter.ChatRequest, io.Writer) error {
	return adapter.ErrNotImplemented
}
func (*recordingAdapter) Validate(context.Context) error     { return nil }
func (*recordingAdapter) Capabilities() adapter.Capabilities { return adapter.Capabilities{Chat: true} }

func TestPrepareRewritesContextCeilingAndEffortSibling(t *testing.T) {
	g := &Gateway{models: []catalog.Model{
		{ID: "gpt-5.6-sol", ContextWindow: 272000, MaxContextWindow: 1050000, Exposed: true, Routable: true},
		{ID: "gemini-3.8-flash-low", Exposed: true, Routable: true},
		{ID: "gemini-3.8-flash-medium", Exposed: true, Routable: true},
		{ID: "gemini-3.8-flash-high", Exposed: true, Routable: true},
	}}
	listed := g.Listed(catalog.FilterAll)
	var sawCeiling, sawBase bool
	for _, m := range listed {
		if m.ID == "gpt-5.6-sol@1050000" && m.ContextWindow == 1050000 {
			sawCeiling = true
		}
		if m.ID == "gemini-3.8-flash" && m.AliasOf == "gemini-3.8-flash-medium" {
			sawBase = true
		}
	}
	if !sawCeiling || !sawBase {
		t.Fatalf("listed ids missing a choice: %+v", listed)
	}

	upstream, client, body, _, err := g.prepare([]byte(`{"model":"gpt-5.6-sol@1050000","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if client != "gpt-5.6-sol@1050000" || upstream != "gpt-5.6-sol" || !strings.Contains(string(body), `"model":"gpt-5.6-sol"`) {
		t.Fatalf("ceiling: client=%s upstream=%s body=%s", client, upstream, body)
	}

	upstream, client, body, _, err = g.prepare([]byte(`{"model":"gemini-3.8-flash","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if client != "gemini-3.8-flash" || upstream != "gemini-3.8-flash-high" || !strings.Contains(string(body), `"model":"gemini-3.8-flash-high"`) {
		t.Fatalf("effort: client=%s upstream=%s body=%s", client, upstream, body)
	}
}

// The Responses and Messages wires spell effort differently from chat. A
// sibling choice that only read reasoning_effort sent every Codex and Claude
// Code request for an effort base to the medium sibling.
func TestPrepareReadsEffortOnEveryWire(t *testing.T) {
	g := &Gateway{models: []catalog.Model{
		{ID: "gemini-3.8-flash-low", Exposed: true, Routable: true},
		{ID: "gemini-3.8-flash-medium", Exposed: true, Routable: true},
		{ID: "gemini-3.8-flash-high", Exposed: true, Routable: true},
	}}
	for _, tc := range []struct{ wire, body string }{
		{"chat", `{"model":"gemini-3.8-flash","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`},
		{"responses", `{"model":"gemini-3.8-flash","reasoning":{"effort":"high"},"input":"hi"}`},
		{"messages", `{"model":"gemini-3.8-flash","max_tokens":64,"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(tc.wire, func(t *testing.T) {
			upstream, client, _, _, err := g.prepare([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if client != "gemini-3.8-flash" || upstream != "gemini-3.8-flash-high" {
				t.Fatalf("client=%s upstream=%s, want the high sibling", client, upstream)
			}
		})
	}
}
