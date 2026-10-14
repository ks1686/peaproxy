package antigravity

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

	adp, err := New(adapter.Options{ID: "antigravity"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adp.(adapter.Authenticator).AuthStart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("got %v, want callback port in use", err)
	}
}
