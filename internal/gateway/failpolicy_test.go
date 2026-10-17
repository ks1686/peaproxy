package gateway

import (
	"context"
	"io"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/router"
)

// nativeCounter is a native Messages and Responses account that fails every
// call with err and counts them.
type nativeCounter struct {
	slowNative
	err error
}

func (a *nativeCounter) Chat(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
	a.calls.Add(1)
	return adapter.ChatResponse{}, a.err
}
func (a *nativeCounter) ChatStream(context.Context, adapter.ChatRequest, io.Writer) error {
	a.calls.Add(1)
	return a.err
}
func (a *nativeCounter) Responses(context.Context, []byte) ([]byte, error) {
	a.calls.Add(1)
	return nil, a.err
}
func (a *nativeCounter) ResponsesStream(context.Context, []byte, io.Writer) error {
	a.calls.Add(1)
	return a.err
}
func (a *nativeCounter) Messages(context.Context, []byte) ([]byte, error) {
	a.calls.Add(1)
	return nil, a.err
}
func (a *nativeCounter) MessagesStream(context.Context, []byte, io.Writer) error {
	a.calls.Add(1)
	return a.err
}

// TestClaudeNativeAdmissionDoesNotCooldown: a native Messages account that is
// busy is skipped for the next account, and is not cooled down for it.
func TestClaudeNativeAdmissionDoesNotCooldown(t *testing.T) {
	first := &nativeCounter{slowNative: slowNative{id: "first"}, err: router.ErrAdmissionQueue}
	second := &nativeCounter{slowNative: slowNative{id: "second"}}
	reg := adapter.NewRegistry()
	reg.Register("native-a", func(adapter.Options) (adapter.Adapter, error) { return first, nil })
	reg.Register("native-b", func(adapter.Options) (adapter.Adapter, error) { return second, nil })
	gw, err := New(config.Config{SchemaVersion: 1, Failover: config.FailoverPrefs{Policy: "fill-first"}, Providers: []config.Provider{
		{ID: "first", Adapter: "native-a", Tier: "paid"},
		{ID: "second", Adapter: "native-b", Tier: "paid"},
	}}, "", reg)
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())

	_, call, err := gw.ClaudeChat(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil || call.Account != "second" {
		t.Fatalf("err=%v account=%q, want served by second", err, call.Account)
	}
	for _, c := range gw.Cooldowns() {
		if c.AccountID == "first" {
			t.Fatalf("admission rejection cooled down first: %+v", c)
		}
	}
}

// TestResponsesNativeProtocolRefusalDoesNotCooldown: a native Responses account
// that refuses the protocol is not cooled down, and the model is marked not-chat.
func TestResponsesNativeProtocolRefusalDoesNotCooldown(t *testing.T) {
	refusal := adapter.HTTPError{Status: 400, Body: protocolUnsupported}
	first := &nativeCounter{slowNative: slowNative{id: "first"}, err: refusal}
	second := &nativeCounter{slowNative: slowNative{id: "second"}}
	reg := adapter.NewRegistry()
	reg.Register("native-a", func(adapter.Options) (adapter.Adapter, error) { return first, nil })
	reg.Register("native-b", func(adapter.Options) (adapter.Adapter, error) { return second, nil })
	gw, err := New(config.Config{SchemaVersion: 1, Failover: config.FailoverPrefs{Policy: "fill-first"}, Providers: []config.Provider{
		{ID: "first", Adapter: "native-a", Tier: "paid"},
		{ID: "second", Adapter: "native-b", Tier: "paid"},
	}}, "", reg)
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())

	_, call, err := gw.Responses(context.Background(), []byte(`{"model":"m","input":"hi"}`))
	if err != nil || call.Account != "second" {
		t.Fatalf("err=%v account=%q, want served by second", err, call.Account)
	}
	for _, c := range gw.Cooldowns() {
		if c.AccountID == "first" {
			t.Fatalf("protocol refusal cooled down first: %+v", c)
		}
	}
	if !gw.notChatModel("first", "m") {
		t.Fatal("protocol refusal did not mark the model not-chat on the refusing account")
	}
}
