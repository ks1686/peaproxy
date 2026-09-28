package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientMutationRejectsForeignOrigin(t *testing.T) {
	s, _ := testServer(t)
	s.clientRoot = t.TempDir()
	req := httptest.NewRequest(http.MethodPost, "/admin/clients/opencode/connect", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://evil.example")
	req.Host = "127.0.0.1:8317"
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
}

func TestClientMutationRejectsUnknownName(t *testing.T) {
	s, _ := testServer(t)
	s.clientRoot = t.TempDir()
	req := httptest.NewRequest(http.MethodPost, "/admin/clients/not-a-client/connect", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "unknown") {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
}

func TestEngineStatusRedactsSecrets(t *testing.T) {
	s, _ := testServer(t)
	cfg := s.gw.Config()
	cfg.AdminToken = "super-secret-admin-token"
	s.gw.SetConfig(cfg)
	req := httptest.NewRequest(http.MethodGet, "/admin/engine", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "super-secret-admin-token") {
		t.Fatalf("secret leaked: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"promptCache":"preserve"`) {
		t.Fatalf("body %s", rr.Body.String())
	}
}

func TestUIConnectControl(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/ui/app.js", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "data-connect") {
		t.Fatalf("status %d", rr.Code)
	}
}

func TestClientConnectUsesIsolatedRoot(t *testing.T) {
	s, _ := testServer(t)
	root := t.TempDir()
	s.clientRoot = root
	req := httptest.NewRequest(http.MethodPost, "/admin/clients/opencode/connect", strings.NewReader(`{"model":"m"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(root, "opencode.json"))
	if err != nil || !strings.Contains(string(got), "peaproxy") {
		t.Fatalf("file %s err %v", got, err)
	}
}
