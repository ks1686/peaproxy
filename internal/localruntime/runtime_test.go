package localruntime

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoadingIsNotOffline(t *testing.T) {
	if got := ClassifyOllama(http.StatusOK, `{"status":"loading model"}`); got != StateLoading {
		t.Fatalf("state %s", got)
	}
}

func TestUnknownRuntimeDoesNotClaimLoaded(t *testing.T) {
	if got := ClassifyOllama(http.StatusOK, ""); got != StateUnknown {
		t.Fatalf("state %s", got)
	}
}

func TestBusyQueueCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("busy"))
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snap := ProbeLoopback(ctx, srv.URL, srv.Client())
	if snap.State != StateOffline && snap.State != StateBusy && snap.State != StateUnknown {
		t.Fatalf("state %s", snap.State)
	}
}

func TestNonLoopbackIsNotProbed(t *testing.T) {
	snap := ProbeLoopback(context.Background(), "http://example.com:11434", nil)
	if snap.State != StateUnknown {
		t.Fatalf("state %s", snap.State)
	}
}

func TestClassifyOllamaStates(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   State
	}{
		{0, "", StateOffline},
		{http.StatusOK, "models", StateReady},
		{http.StatusInternalServerError, "", StateOffline},
		{http.StatusBadRequest, "unrecognized", StateUnknown},
	}
	for _, tc := range cases {
		if got := ClassifyOllama(tc.status, tc.body); got != tc.want {
			t.Errorf("ClassifyOllama(%d, %q) = %s, want %s", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestLoopbackHelpersAndTags(t *testing.T) {
	for _, raw := range []string{"http://localhost:11434", "http://127.0.0.1:11434", "http://[::1]:11434", "127.0.0.1:11434"} {
		if !LoopbackURL(raw) {
			t.Errorf("LoopbackURL(%q) = false", raw)
		}
	}
	for _, raw := range []string{"", "https://example.com:11434", "http://192.168.1.2:11434"} {
		if LoopbackURL(raw) {
			t.Errorf("LoopbackURL(%q) = true", raw)
		}
	}
	if !ExactLocalStaysLocal(true) || ExactLocalStaysLocal(false) {
		t.Fatal("exact-local decision changed")
	}
	if !ParseTagsReady([]byte(`{"models":[{"name":"m"}]}`)) || ParseTagsReady([]byte(`{"models":[]}`)) || ParseTagsReady([]byte(`bad`)) {
		t.Fatal("tags readiness classification incorrect")
	}
	if PortOpen(0) || PortOpen(65536) {
		t.Fatal("invalid ports must be closed")
	}
}

func TestProbeLoopbackReadyAndPortOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"m"}]}`))
	}))
	t.Cleanup(srv.Close)
	if snap := ProbeLoopback(context.Background(), srv.URL, srv.Client()); snap.State != StateReady || snap.Endpoint != srv.URL || snap.At.IsZero() {
		t.Fatalf("snapshot = %#v", snap)
	}
	if !PortOpen(srv.Listener.Addr().(*net.TCPAddr).Port) {
		t.Fatal("test server port should be open")
	}
}
