package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #75: a loopback peer sending a non-loopback Host is the DNS-rebinding
// signature. It was only checked on mutations, so /admin/usage,
// /admin/requests, /admin/accounts and /admin/settings could be read by a page
// whose DNS points at 127.0.0.1.
func TestAdminReadsRequireALoopbackHost(t *testing.T) {
	srv, _ := testServer(t)
	for _, path := range []string{
		"/admin/usage",
		"/admin/requests",
		"/admin/accounts",
		"/admin/settings",
		"/admin/health",
		"/admin/models",
		"/admin/clients",
		"/admin/presets",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "evil.example.com"
		req.RemoteAddr = "127.0.0.1:5555"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s with a rebound host returned %d, want 403", path, rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, `"usage"`) || strings.Contains(body, `"accounts"`) {
			t.Errorf("GET %s returned data to a rebound host: %s", path, body)
		}
	}
}

// The proxy API reads take the same rule. /v1/models is unauthenticated by
// design on loopback, and it is still a read.
func TestProxyReadsRequireALoopbackHost(t *testing.T) {
	srv, _ := testServer(t)
	for _, path := range []string{"/v1/models", "/healthz", "/admin/catalog"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "evil.example.com"
		req.RemoteAddr = "127.0.0.1:5555"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s with a rebound host returned %d, want 403", path, rec.Code)
		}
	}
}

// HEAD and OPTIONS were in the early return too.
func TestHeadAndOptionsRequireALoopbackHost(t *testing.T) {
	srv, _ := testServer(t)
	for _, method := range []string{http.MethodHead, http.MethodOptions} {
		req := httptest.NewRequest(method, "/admin/settings", nil)
		req.Host = "evil.example.com"
		req.RemoteAddr = "127.0.0.1:5555"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s /admin/settings with a rebound host returned %d, want 403", method, rec.Code)
		}
	}
}

// Normal use must not break: a loopback Host is fine, in every spelling a
// browser or a CLI actually sends.
func TestLoopbackHostsStillWorkForReads(t *testing.T) {
	srv, _ := testServer(t)
	for _, host := range []string{"127.0.0.1:8317", "localhost:8317", "[::1]:8317", "127.0.0.1", "localhost"} {
		req := httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
		req.Host = host
		req.RemoteAddr = "127.0.0.1:5555"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden {
			t.Errorf("Host %q was rejected: %s", host, rec.Body.String())
		}
	}
}

// A LAN peer is not subject to the loopback Host rule: that check exists to
// stop a browser on this machine, and with --allow-lan the adminToken is the
// control.
func TestNonLoopbackPeerIsNotSubjectToTheLoopbackHostRule(t *testing.T) {
	srv, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	req.Host = "peaproxy.lan:8317"
	req.RemoteAddr = "192.168.1.20:5555"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "loopback host") {
		t.Errorf("a LAN peer was asked for a loopback Host: %d %s", rec.Code, rec.Body.String())
	}
}
