package adapter

import (
	"net/http"
	"time"
)

// HTTPClient builds a client that notifies ObserveHeaders with a clone of each
// upstream response's headers. The callback must return quickly (no extra I/O).
func HTTPClient(timeout time.Duration, observe func(http.Header)) *http.Client {
	c := &http.Client{Timeout: timeout}
	if observe == nil {
		return c
	}
	c.Transport = observeTransport{base: http.DefaultTransport, observe: observe}
	return c
}

type observeTransport struct {
	base    http.RoundTripper
	observe func(http.Header)
}

func (t observeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil || t.observe == nil {
		return resp, err
	}
	t.observe(resp.Header.Clone())
	return resp, nil
}
