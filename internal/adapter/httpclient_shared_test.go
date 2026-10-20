package adapter

import (
	"net/http"
	"testing"
)

// Adapters that rebuild their inner client per request (the subscription-OAuth
// family) got a fresh transport each time: a new TCP+TLS handshake per call
// and an idle connection stranded in a pool nobody reused.
func TestHTTPClientsShareOneConnectionPool(t *testing.T) {
	a := HTTPClient(0, nil).Transport.(lifecycleTransport)
	b := HTTPClient(0, func(http.Header) {}).Transport.(lifecycleTransport)
	if a.base != b.base {
		t.Fatal("each HTTPClient built its own transport; connections are never reused across clients")
	}
}
