package localassistant

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ks1686/peaproxy/internal/localruntime"
)

// The point of the local assistant is that the user's prompts stay on the
// machine. That promise is worthless if a misconfigured endpoint can point
// anywhere, so a non-loopback address is refused outright rather than warned
// about.
func TestNonLoopbackEndpointIsRefused(t *testing.T) {
	for _, endpoint := range []string{
		"http://192.168.1.10:11234/v1",
		"https://api.openai.com/v1",
		"http://0.0.0.0:11234/v1",
	} {
		a, err := New(Config{Endpoint: endpoint})
		if err == nil {
			t.Errorf("%s was accepted; it is not loopback", endpoint)
			continue
		}
		if a != nil {
			t.Errorf("%s returned an assistant alongside an error", endpoint)
		}
	}
}

// A loopback endpoint in an accepted spelling is not refused for the wrong
// reason. Getting this backwards would make the feature unusable.
func TestLoopbackEndpointsAreAccepted(t *testing.T) {
	for _, endpoint := range []string{
		"http://127.0.0.1:11234/v1",
		"http://localhost:11234/v1",
		"http://[::1]:11234/v1",
		"http://127.0.0.1:11434/v1/chat/completions",
	} {
		if _, err := New(Config{Endpoint: endpoint, Enabled: true}); err != nil {
			t.Errorf("%s was refused: %v", endpoint, err)
		}
	}
}

// Nothing runs unless the user asked. The probe is the first thing this
// component does, so an unconfigured assistant must not make it.
func TestDisabledAssistantNeverContactsAnything(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	// A disabled assistant is not a value that can be misused later: there is
	// none. Nothing holds an HTTP client, so no later code path can reach the
	// endpoint by accident.
	a, err := New(Config{Endpoint: srv.URL, Enabled: false})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled assistant error = %v, want ErrDisabled", err)
	}
	if a != nil {
		t.Fatal("a disabled assistant returned a usable value")
	}
	if calls != 0 {
		t.Fatalf("a disabled assistant made %d request(s)", calls)
	}

	// A nil assistant is safe to ask anyway, which matters because callers hold
	// it long after construction.
	var nilA *Assistant
	if ready, err := nilA.Ready(); ready || !errors.Is(err, ErrDisabled) {
		t.Fatalf("nil assistant Ready = %v, %v", ready, err)
	}
	if calls != 0 {
		t.Fatalf("a nil assistant made %d request(s)", calls)
	}
}

// An enabled assistant whose endpoint is not running is unavailable, not an
// error the caller must handle specially. Nothing is downloaded and nothing is
// started on the user's behalf.
func TestEnabledButUnreachableIsUnavailableNotBroken(t *testing.T) {
	a, err := New(Config{Endpoint: "http://127.0.0.1:1/v1", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := a.Ready()
	if err != nil {
		t.Fatalf("unreachable should be a clean unavailable, got %v", err)
	}
	if ready {
		t.Fatal("an unreachable endpoint reported ready")
	}
}

// A running endpoint that is not a local model server must not be mistaken for
// one. PeaProxy sends prompts to this address, so it has to be sure what is
// listening.
func TestRunningButUnrecognisedEndpointIsNotReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>hello</html>"))
	}))
	defer srv.Close()

	a, err := New(Config{Endpoint: srv.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if ready, _ := a.Ready(); ready {
		t.Fatal("an endpoint serving HTML was accepted as a local model server")
	}
}

var _ = localruntime.KnownLoopbackPorts
