package adapter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPClientObservesResponseHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-remaining-requests", "7")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	c := HTTPClient(5*time.Second, func(h http.Header) { got = h })
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got.Get("x-ratelimit-remaining-requests") != "7" {
		t.Fatalf("%v", got)
	}
}

func TestHTTPClientKeepsStreamTimeoutAndTLSVerify(t *testing.T) {
	c := HTTPClient(0, nil)
	if c.Timeout != 0 {
		t.Fatal("zero timeout must stay unbounded for streams")
	}
	tr, ok := c.Transport.(lifecycleTransport)
	if !ok {
		t.Fatalf("%T", c.Transport)
	}
	base, ok := tr.base.(*http.Transport)
	if !ok {
		t.Fatalf("%T", tr.base)
	}
	if base.TLSClientConfig != nil && base.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("tls verification disabled")
	}
	if base.Proxy == nil {
		t.Fatal("proxy from environment missing")
	}
}

func TestLongActiveStreamSurvives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, "data: ok\n\n")
	}))
	t.Cleanup(srv.Close)
	resp, err := HTTPClient(0, nil).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(got), "ok") {
		t.Fatalf("body %q err %v", got, err)
	}
}

func TestFailedAttemptClosesBody(t *testing.T) {
	body := &closeTracker{Reader: strings.NewReader("no")}
	req, err := http.NewRequest(http.MethodGet, "http://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (lifecycleTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: body, Header: make(http.Header), Request: req}, nil
	})}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 300 {
		t.Fatal("expected failure")
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if !body.closed {
		t.Fatal("failed attempt left the body open")
	}
}

type closeTracker struct {
	io.Reader
	closed bool
}

func (c *closeTracker) Close() error {
	c.closed = true
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPClientCancelClosesUpstream(t *testing.T) {
	canceled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
		close(canceled)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := HTTPClient(0, nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request was not canceled")
	}
	_ = resp.Body.Close()
}
