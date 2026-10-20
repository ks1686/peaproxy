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
	return &http.Client{
		Timeout:   timeout,
		Transport: lifecycleTransport{base: sharedTransport, observe: observe},
	}
}

// sharedTransport is the one connection pool every adapter client uses. The
// settings were identical per client anyway, and adapters that rebuild their
// inner client per request (the subscription-OAuth family) used to get a fresh
// transport each time: a new TCP+TLS handshake per call and an idle connection
// stranded in a pool nobody reused. http.Transport is safe for concurrent use.
var sharedTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          128,
	MaxIdleConnsPerHost:   16,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 2 * time.Minute,
	ExpectContinueTimeout: time.Second,
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
