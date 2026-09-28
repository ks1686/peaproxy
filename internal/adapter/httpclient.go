package adapter

import (
	"net"
	"net/http"
	"time"
)

// HTTPClient builds a client that notifies ObserveHeaders with a clone of each
// upstream response's headers. The callback must return quickly (no extra I/O).
// A zero timeout leaves the body unbounded so streams are not cut off. Header
// and dial deadlines still apply, and a canceled caller context closes the body.
func HTTPClient(timeout time.Duration, observe func(http.Header)) *http.Client {
	base := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: lifecycleTransport{base: base, observe: observe},
	}
}

type lifecycleTransport struct {
	base    http.RoundTripper
	observe func(http.Header)
}

func (t lifecycleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if req.Context().Err() != nil {
		_ = resp.Body.Close()
		return nil, req.Context().Err()
	}
	if t.observe != nil {
		t.observe(resp.Header.Clone())
	}
	return resp, nil
}
