package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
)

// slowOAuth completes login at once, then blocks ListModels until released so
// a test can observe the job state while the post-login refresh is in flight.
type slowOAuth struct {
	completeErr error
	listed      chan context.Context
	release     chan struct{}
}

func (a *slowOAuth) ID() string { return "slow" }

func (a *slowOAuth) ListModels(ctx context.Context) ([]catalog.Model, error) {
	a.listed <- ctx
	<-a.release
	return nil, nil
}

func (a *slowOAuth) Chat(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
	return adapter.ChatResponse{}, adapter.ErrNotImplemented
}

func (a *slowOAuth) ChatStream(context.Context, adapter.ChatRequest, io.Writer) error {
	return adapter.ErrNotImplemented
}

func (a *slowOAuth) Validate(context.Context) error     { return nil }
func (a *slowOAuth) Capabilities() adapter.Capabilities { return adapter.Capabilities{OAuth: true} }
func (a *slowOAuth) AuthStart(context.Context) (adapter.AuthSession, error) {
	return adapter.AuthSession{LoginURL: "http://127.0.0.1/login"}, nil
}

func (a *slowOAuth) AuthComplete(context.Context, adapter.AuthSession, string) error {
	return a.completeErr
}

func oauthServer(t *testing.T, fake *slowOAuth) *Server {
	t.Helper()
	reg := adapter.NewRegistry()
	reg.Register("slow_oauth", func(adapter.Options) (adapter.Adapter, error) { return fake, nil })
	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "slow", Adapter: "slow_oauth", Tier: "paid"}}
	gw, err := gateway.New(cfg, "", reg)
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Gateway: gw})
}

func oauthCall(t *testing.T, s *Server, method, target, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, target, rr.Code, rr.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOAuthLoginCompletesBeforeUnboundedCatalogRefresh(t *testing.T) {
	fake := &slowOAuth{listed: make(chan context.Context, 1), release: make(chan struct{})}
	t.Cleanup(func() { close(fake.release) })
	s := oauthServer(t, fake)
	oauthCall(t, s, http.MethodPost, "/admin/oauth/start", `{"id":"slow"}`)
	var ctx context.Context
	select {
	case ctx = <-fake.listed:
	case <-time.After(5 * time.Second):
		t.Fatal("post-login refresh never ran")
	}
	if dl, ok := ctx.Deadline(); ok {
		t.Fatalf("refresh bounded by our deadline %s; one slow account would drop the others", time.Until(dl).Round(time.Second))
	}
	got := oauthCall(t, s, http.MethodGet, "/admin/oauth/status?id=slow", "")
	if got["status"] != "complete" {
		t.Fatalf("login must read complete while the refresh runs, got %v", got["status"])
	}
}

func TestOAuthLoginErrorSkipsRefresh(t *testing.T) {
	fake := &slowOAuth{completeErr: errors.New("denied"), listed: make(chan context.Context, 1), release: make(chan struct{})}
	t.Cleanup(func() { close(fake.release) })
	s := oauthServer(t, fake)
	oauthCall(t, s, http.MethodPost, "/admin/oauth/start", `{"id":"slow"}`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := oauthCall(t, s, http.MethodGet, "/admin/oauth/status?id=slow", "")
		if got["status"] == "error" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status %v", got["status"])
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-fake.listed:
		t.Fatal("failed login must not refresh the catalog")
	default:
	}
}
