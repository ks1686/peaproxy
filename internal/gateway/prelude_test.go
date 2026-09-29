package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/router"
)

type streamCall func(*Gateway, context.Context, []byte, io.Writer) (string, error)

func TestLastNativeAccountUnboundedWhenTranslationFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		call  streamCall
		body  string
		event string
	}{
		{"responses", (*Gateway).ResponsesStream, `{"model":"m","stream":true,"input":[]}`, "response.created"},
		{"messages", (*Gateway).ClaudeChatStream, `{"model":"m","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`, "message_start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			native := &slowNative{id: "native", delay: 200 * time.Millisecond}
			compatHits := 0
			srv := httptest.NewServer(countOK(&compatHits, "compat"))
			t.Cleanup(srv.Close)
			reg := adapters.DefaultRegistry()
			reg.Register("native", func(adapter.Options) (adapter.Adapter, error) { return native, nil })
			cfg := config.Config{SchemaVersion: 1, Failover: config.FailoverPrefs{Policy: "fill-first"}, Providers: []config.Provider{
				{ID: "native", Adapter: "native", Tier: "paid"},
				{ID: "compat", Adapter: "openai_compat", Tier: "paid", BaseURL: srv.URL + "/v1"},
			}}
			cfg.RequestEngine.PreludeTimeout = "20ms"
			gw, err := New(cfg, "", reg)
			if err != nil {
				t.Fatal(err)
			}
			gw.Refresh(context.Background())

			var out strings.Builder
			account, err := tc.call(gw, context.Background(), []byte(tc.body), &out)

			if err != nil || account != "native" {
				t.Fatalf("prelude cut off the only account that can serve this body: account=%q err=%v", account, err)
			}
			if !strings.Contains(out.String(), tc.event) || native.calls.Load() != 1 || compatHits != 0 {
				t.Fatalf("out=%q native calls=%d compat hits=%d", out.String(), native.calls.Load(), compatHits)
			}
		})
	}
}

func TestSlowSkipIsNotReportedAsAllCooled(t *testing.T) {
	rateLimited := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}
	for _, tc := range []struct {
		name string
		call streamCall
		body string
	}{
		{"chat", (*Gateway).ChatStream, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", (*Gateway).ResponsesStream, `{"model":"m","stream":true,"input":"hi"}`},
		{"messages", (*Gateway).ClaudeChatStream, `{"model":"m","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			slowHits := 0
			gw := twoAccountGateway(t, slowStream(200*time.Millisecond, &slowHits, &mu), rateLimited)
			gw.cfg.Failover.Policy = "fill-first"
			gw.cfg.RequestEngine.PreludeTimeout = "20ms"

			_, err := tc.call(gw, context.Background(), []byte(tc.body), io.Discard)

			if errors.As(err, &router.CooldownError{}) {
				t.Fatalf("slow account was skipped, not cooled, yet the error says all accounts are cooling: %v", err)
			}
			var he adapter.HTTPError
			if !errors.As(err, &he) || he.Status != http.StatusTooManyRequests {
				t.Fatalf("error = %v, want the upstream 429", err)
			}
			for _, c := range gw.Cooldowns() {
				if c.AccountID == "acct-a" {
					t.Fatalf("slow prelude cooled acct-a: %+v", c)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if slowHits != 1 {
				t.Fatalf("slow account hits = %d, want 1", slowHits)
			}
		})
	}
}

// slowNative serves Responses and Messages natively and holds its first
// event for delay unless the attempt context ends first.
type slowNative struct {
	id    string
	delay time.Duration
	calls atomic.Int32
}

func (a *slowNative) ID() string { return a.id }
func (a *slowNative) ListModels(context.Context) ([]catalog.Model, error) {
	return []catalog.Model{{ID: "m", AccountID: a.id, Provider: "native", Tier: catalog.TierPaid}}, nil
}
func (a *slowNative) Chat(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
	return adapter.ChatResponse{}, adapter.ErrNotImplemented
}
func (a *slowNative) ChatStream(context.Context, adapter.ChatRequest, io.Writer) error {
	return adapter.ErrNotImplemented
}
func (a *slowNative) Validate(context.Context) error { return nil }
func (a *slowNative) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{Chat: true, Stream: true}
}
func (a *slowNative) Responses(context.Context, []byte) ([]byte, error) {
	return nil, adapter.ErrNotImplemented
}
func (a *slowNative) ResponsesStream(ctx context.Context, _ []byte, w io.Writer) error {
	return a.first(ctx, w, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
}
func (a *slowNative) Messages(context.Context, []byte) ([]byte, error) {
	return nil, adapter.ErrNotImplemented
}
func (a *slowNative) MessagesStream(ctx context.Context, _ []byte, w io.Writer) error {
	return a.first(ctx, w, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
}

func (a *slowNative) first(ctx context.Context, w io.Writer, event string) error {
	a.calls.Add(1)
	select {
	case <-time.After(a.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	_, err := io.WriteString(w, event)
	return err
}
