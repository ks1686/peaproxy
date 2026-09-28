package localruntime

import (
	"context"
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
