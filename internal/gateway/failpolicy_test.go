package gateway

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
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

// chatOnly is an account with no native Responses or Messages wire, so the
// gateway reaches it through the translated tail. A nil err answers.
type chatOnly struct {
	id  string
	err error
}

func (a *chatOnly) ID() string { return a.id }
func (a *chatOnly) ListModels(context.Context) ([]catalog.Model, error) {
	return []catalog.Model{{ID: "m", AccountID: a.id, Provider: "chat", Tier: catalog.TierPaid}}, nil
}
func (a *chatOnly) Chat(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
	if a.err != nil {
		return adapter.ChatResponse{}, a.err
	}
	return adapter.ChatResponse{Raw: []byte(`{"id":"c","model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`), Content: "ok"}, nil
}
func (a *chatOnly) ChatStream(context.Context, adapter.ChatRequest, io.Writer) error {
	return adapter.ErrNotImplemented
}
func (a *chatOnly) Validate(context.Context) error { return nil }
func (a *chatOnly) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{Chat: true}
}

func translatedGateway(t *testing.T, accounts ...*chatOnly) *Gateway {
	t.Helper()
	reg := adapter.NewRegistry()
	var providers []config.Provider
	for _, a := range accounts {
		a := a
		reg.Register("chat-"+a.id, func(adapter.Options) (adapter.Adapter, error) { return a, nil })
		providers = append(providers, config.Provider{ID: a.id, Adapter: "chat-" + a.id, Tier: "paid"})
	}
	gw, err := New(config.Config{SchemaVersion: 1, Failover: config.FailoverPrefs{Policy: "fill-first"}, Providers: providers}, "", reg)
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	return gw
}

var translatedWires = []struct {
	name string
	call func(*Gateway) (Call, error)
}{
	{"responses", func(gw *Gateway) (Call, error) {
		_, call, err := gw.Responses(context.Background(), []byte(`{"model":"m","input":"hi"}`))
		return call, err
	}},
	{"messages", func(gw *Gateway) (Call, error) {
		_, call, err := gw.ClaudeChat(context.Background(), []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
		return call, err
	}},
}

// The translated tails of Responses and ClaudeChat take the same non-stream
// order as Chat: a busy account is skipped without a cooldown, and a protocol
// refusal marks the model instead of cooling the account.
func TestTranslatedTailsUseTheNonStreamPolicy(t *testing.T) {
	for _, wire := range translatedWires {
		for _, tc := range []struct {
			name    string
			err     error
			refused bool
		}{
			{"admission", router.ErrAdmissionQueue, false},
			{"protocol refusal", adapter.HTTPError{Status: 400, Body: protocolUnsupported}, true},
		} {
			t.Run(wire.name+" "+tc.name, func(t *testing.T) {
				gw := translatedGateway(t, &chatOnly{id: "first", err: tc.err}, &chatOnly{id: "second"})
				call, err := wire.call(gw)
				if err != nil || call.Account != "second" {
					t.Fatalf("err=%v account=%q, want served by second", err, call.Account)
				}
				for _, c := range gw.Cooldowns() {
					if c.AccountID == "first" {
						t.Fatalf("%s cooled down first: %+v", tc.name, c)
					}
				}
				if got := gw.notChatModel("first", "m"); got != tc.refused {
					t.Fatalf("notChatModel = %v, want %v", got, tc.refused)
				}
			})
		}
	}
}

// When every account refuses the protocol, Responses and ClaudeChat say so,
// as Chat does, instead of passing one provider's 400 through.
func TestEveryAccountRefusingIsModelRefusesChat(t *testing.T) {
	for _, wire := range translatedWires {
		t.Run(wire.name, func(t *testing.T) {
			gw := translatedGateway(t, &chatOnly{id: "only", err: adapter.HTTPError{Status: 400, Body: protocolUnsupported}})
			if _, err := wire.call(gw); !errors.Is(err, errModelRefusesChat) {
				t.Fatalf("err = %v, want errModelRefusesChat", err)
			}
		})
	}
	t.Run("native responses", func(t *testing.T) {
		only := &nativeCounter{slowNative: slowNative{id: "only"}, err: adapter.HTTPError{Status: 400, Body: protocolUnsupported}}
		reg := adapter.NewRegistry()
		reg.Register("native", func(adapter.Options) (adapter.Adapter, error) { return only, nil })
		gw, err := New(config.Config{SchemaVersion: 1, Providers: []config.Provider{{ID: "only", Adapter: "native", Tier: "paid"}}}, "", reg)
		if err != nil {
			t.Fatal(err)
		}
		gw.Refresh(context.Background())
		if _, _, err := gw.Responses(context.Background(), []byte(`{"model":"m","input":"hi"}`)); !errors.Is(err, errModelRefusesChat) {
			t.Fatalf("err = %v, want errModelRefusesChat", err)
		}
	})
}
