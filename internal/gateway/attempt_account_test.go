package gateway

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/config"
)

type failingNative struct {
	slowNative
	err error
}

func (a *failingNative) Chat(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
	a.calls.Add(1)
	return adapter.ChatResponse{}, a.err
}
func (a *failingNative) ChatStream(context.Context, adapter.ChatRequest, io.Writer) error {
	a.calls.Add(1)
	return a.err
}
func (a *failingNative) Responses(context.Context, []byte) ([]byte, error) {
	a.calls.Add(1)
	return nil, a.err
}
func (a *failingNative) Messages(context.Context, []byte) ([]byte, error) {
	a.calls.Add(1)
	return nil, a.err
}
func (a *failingNative) ResponsesStream(context.Context, []byte, io.Writer) error {
	a.calls.Add(1)
	return a.err
}
func (a *failingNative) MessagesStream(context.Context, []byte, io.Writer) error {
	a.calls.Add(1)
	return a.err
}

type compatOnly struct{ adapter.Adapter }

func TestLastAccountExcludesUnattemptedCandidates(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		call streamCall
	}{
		{"responses_stream", `{"model":"m","input":[]}`, (*Gateway).ResponsesStream},
		{"messages_stream", `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`, (*Gateway).ClaudeChatStream},
		{"responses", `{"model":"m","input":[]}`, func(g *Gateway, ctx context.Context, raw []byte, _ io.Writer) (string, error) {
			_, account, err := g.Responses(ctx, raw)
			return account, err
		}},
		{"messages", `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`, func(g *Gateway, ctx context.Context, raw []byte, _ io.Writer) (string, error) {
			_, account, err := g.ClaudeChat(ctx, raw)
			return account, err
		}},
		{"chat_stream_budget", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, (*Gateway).ChatStream},
		{"chat_budget", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, func(g *Gateway, ctx context.Context, raw []byte, _ io.Writer) (string, error) {
			_, account, err := g.Chat(ctx, raw)
			return account, err
		}},
	} {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusBadRequest} {
			t.Run(tc.name+"/"+http.StatusText(status), func(t *testing.T) {
				// Given native first, with a compat candidate that must never be called.
				native := &failingNative{slowNative: slowNative{id: "native"}, err: adapter.HTTPError{Status: status}}
				compat := &failingNative{slowNative: slowNative{id: "compat"}, err: adapter.ErrNotImplemented}
				reg := adapter.NewRegistry()
				reg.Register("native", func(adapter.Options) (adapter.Adapter, error) { return native, nil })
				reg.Register("compat", func(adapter.Options) (adapter.Adapter, error) { return compatOnly{compat}, nil })
				cfg := config.Config{Failover: config.FailoverPrefs{Policy: "fill-first"}, Providers: []config.Provider{
					{ID: "native", Adapter: "native"}, {ID: "compat", Adapter: "compat"},
				}}
				cfg.RequestEngine.MaxAttempts = 1
				gw, err := New(cfg, "", reg)
				if err != nil {
					t.Fatal(err)
				}
				gw.Refresh(context.Background())

				// When native fails and compat is skipped for translation or budget.
				account, err := tc.call(gw, context.Background(), []byte(tc.body), io.Discard)

				// Then attribution stays on the sole attempted account.
				if err == nil || native.calls.Load() != 1 || compat.calls.Load() != 0 {
					t.Fatalf("err=%v native calls=%d compat calls=%d", err, native.calls.Load(), compat.calls.Load())
				}
				if account != "native" {
					t.Fatalf("account=%q, want native (compat was never called)", account)
				}
			})
		}
	}
}
