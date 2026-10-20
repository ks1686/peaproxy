package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminHideRefusesOversizedBody(t *testing.T) {
	s, _ := testServer(t)
	body := `{"kind":"model","hidden":true,"id":"` + strings.Repeat("a", 2<<20) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/hide", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:4321"
	req.Host = "127.0.0.1:8317"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413 for a body over the admin limit: %s", rr.Code, rr.Body.String())
	}
}
