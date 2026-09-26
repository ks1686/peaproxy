package adapter

import (
	"net/http"
	"net/http/httptest"
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

func TestHTTPClientNilObserveIsPlain(t *testing.T) {
	c := HTTPClient(0, nil)
	if c.Transport != nil {
		t.Fatal("plain client should keep default transport")
	}
}
