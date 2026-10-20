package anthropic_oauth

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
)

func TestAuthStartFailsWhenCallbackPortIsBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:"+callbackPort)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	adp, err := New(adapter.Options{ID: "anthropic-oauth"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adp.(adapter.Authenticator).AuthStart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("got %v, want callback port in use", err)
	}
}

// Retrying a login (a closed tab, a fresh URL) must not fail on PeaProxy's own
// listener from the previous attempt, which waits up to five minutes.
func TestAuthStartCanBeRetriedWhileAPreviousLoginWaits(t *testing.T) {
	adp, err := New(adapter.Options{ID: "anthropic-oauth"})
	if err != nil {
		t.Fatal(err)
	}
	auth := adp.(adapter.Authenticator)
	if _, err := auth.AuthStart(context.Background()); err != nil {
		t.Skipf("callback port unavailable on this machine: %v", err)
	}
	if _, err := auth.AuthStart(context.Background()); err != nil {
		t.Fatalf("second AuthStart failed while the first login waited: %v", err)
	}
	a := adp.(*Adapter)
	a.mu.Lock()
	if a.pending != nil && a.pending.lb != nil {
		_ = a.pending.lb.Close()
	}
	a.mu.Unlock()
}
