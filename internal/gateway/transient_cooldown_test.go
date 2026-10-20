package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
)

func soleAccountGateway(t *testing.T, h http.HandlerFunc) *Gateway {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := config.Config{SchemaVersion: 1, Providers: []config.Provider{
		{ID: "only", Adapter: "openai_compat", Tier: "paid", BaseURL: srv.URL + "/v1", APIKey: "sk-test"},
	}}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	return gw
}

func listsModelM(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/v1/models" {
		return false
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
	return true
}

var tlsBlip = &url.Error{Op: "Post", URL: "https://api.example.test/v1/messages", Err: errors.New("remote error: tls: bad record MAC")}

// One TLS blip on the only account used to cool it for 5s, so every request
// in that window failed "after failover, Ns left" although nothing was wrong
// with the account and there was nowhere else to send them.
func TestATransportBlipDoesNotCoolTheOnlyAccount(t *testing.T) {
	gw := soleAccountGateway(t, func(w http.ResponseWriter, r *http.Request) { listsModelM(w, r) })
	gw.markCooldown("only", "m", tlsBlip)
	gw.markCooldown("only", "m", adapter.HTTPError{Status: http.StatusBadGateway, Body: "bad gateway"})
	if cs := gw.Cooldowns(); len(cs) != 0 {
		t.Fatalf("a transport failure cooled the only account: %+v", cs)
	}
	if cands, _ := gw.candidates("m", ""); countID(cands, "only") != 1 {
		t.Fatalf("the only account left rotation after a transport blip: %v", ids(cands))
	}
}

// The upstream asking us to wait is not a blip, and neither is a rate limit.
func TestTheOnlyAccountStillCoolsWhenTheUpstreamSaysSo(t *testing.T) {
	for name, err := range map[string]error{
		"retry-after": adapter.HTTPError{Status: http.StatusBadGateway, Body: "bad gateway", RetryAfter: 3 * time.Second},
		"rate limit":  adapter.HTTPError{Status: http.StatusTooManyRequests, Body: `{"error":{"type":"rate_limit_error"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			gw := soleAccountGateway(t, func(w http.ResponseWriter, r *http.Request) { listsModelM(w, r) })
			gw.markCooldown("only", "m", err)
			if len(gw.Cooldowns()) != 1 {
				t.Fatalf("%v did not cool the only account", err)
			}
		})
	}
}

// With another account to steer to, the short transient cooldown stays.
func TestATransportBlipStillCoolsAnAccountWithCompany(t *testing.T) {
	a, b := 0, 0
	gw := twoAccountGateway(t, countOK(&a, "a"), countOK(&b, "b"))
	gw.markCooldown("acct-a", "m", adapter.HTTPError{Status: http.StatusServiceUnavailable, Body: "upstream connect error or disconnect/reset before headers. reset reason: connection timeout"})
	cs := gw.Cooldowns()
	if len(cs) != 1 || cs[0].AccountID != "acct-a" {
		t.Fatalf("cooldowns=%+v, want one on acct-a", cs)
	}
	if cs[0].RemainingMs > 5000 {
		t.Fatalf("edge transport failure cooled the account for %dms, want at most 5s", cs[0].RemainingMs)
	}
}

// signalWriter closes a channel the first time each marker reaches the client.
type signalWriter struct {
	mu      sync.Mutex
	buf     strings.Builder
	markers map[string]chan struct{}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for m, ch := range w.markers {
		if strings.Contains(w.buf.String(), m) {
			close(ch)
			delete(w.markers, m)
		}
	}
	return len(p), nil
}

func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// A recovering account's probe used to be held until its stream ended, so
// while one long stream ran, every other request for a single-account model
// failed "recovering from cooldown, retry shortly". The first valid event
// proves the account works and puts it back in rotation.
func TestAStreamReleasesTheProbeWhenItCommits(t *testing.T) {
	sendSecond := make(chan struct{})
	finish := make(chan struct{})
	gw := soleAccountGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if listsModelM(w, r) {
			return
		}
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first-marker\"}}]}\n\n"))
		flusher.Flush()
		<-sendSecond
		_, _ = w.Write([]byte("data: {\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"second-marker\"}}]}\n\n"))
		flusher.Flush()
		<-finish
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	// Registered after the server's Close, so it runs first: a failed assertion
	// must not leave the handler blocked and Close waiting on it forever.
	var unblock sync.Once
	release := func() { unblock.Do(func() { close(sendSecond); close(finish) }) }
	t.Cleanup(release)
	gw.cool["only"] = cooldownSlots{wide: Cooldown{AccountID: "only", Until: time.Now().Add(-time.Millisecond)}}

	first, second := make(chan struct{}), make(chan struct{})
	out := &signalWriter{markers: map[string]chan struct{}{"first-marker": first, "second-marker": second}}
	done := make(chan error, 1)
	go func() {
		_, err := gw.ChatStream(context.Background(), []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`), out)
		done <- err
	}()
	awaitSignal(t, first, "the first event")
	sendSecond <- struct{}{}
	// The second event is written only after the write that committed the
	// stream returned, and the commit hook runs before that write returns.
	awaitSignal(t, second, "the second event")

	if gw.admission.Probing("only") {
		t.Fatal("the probe is still held by a stream that already committed")
	}
	if cands, _ := gw.candidates("m", ""); countID(cands, "only") != 1 {
		t.Fatalf("a second request could not use the account while the first streams: %v", ids(cands))
	}
	slot, err := gw.admit(context.Background(), "only")
	if err != nil {
		t.Fatalf("a second request was refused while the first streams: %v", err)
	}
	slot()

	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stream failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not finish")
	}
}
