package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/config"
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
	req := httptest.NewRequest(http.MethodPost, "/admin/clients/claude-code/connect", strings.NewReader(`{"model":"m"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil || !strings.Contains(string(got), "peaproxy") {
		t.Fatalf("file %s err %v", got, err)
	}
}

func TestClientConnectOpenCodeIsGuided(t *testing.T) {
	s, _ := testServer(t)
	root := t.TempDir()
	s.clientRoot = root
	req := httptest.NewRequest(http.MethodPost, "/admin/clients/opencode/connect", strings.NewReader(`{"model":"m"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"status":"guided"`) || !strings.Contains(rr.Body.String(), "peaproxy-anthropic") {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "opencode.json")); !os.IsNotExist(err) {
		t.Fatalf("guided connect wrote opencode.json: %v", err)
	}
}

func listenServer(t *testing.T, bind string, port int) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	return New(Options{Config: config.Config{Bind: bind, Port: port}, ClientRoot: root}), root
}

func connectClaudeBaseURL(t *testing.T, s *Server, root string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/clients/claude-code/connect", strings.NewReader(`{"model":""}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("settings invalid: %v\n%s", err, raw)
	}
	return parsed.Env["ANTHROPIC_BASE_URL"]
}

func TestClientConnectUsesListenPort(t *testing.T) {
	s, root := listenServer(t, "127.0.0.1", 9123)
	if got := connectClaudeBaseURL(t, s, root); got != "http://127.0.0.1:9123" {
		t.Fatalf("base url = %q", got)
	}
	s, root = listenServer(t, "127.0.0.1", 0)
	if got := connectClaudeBaseURL(t, s, root); got != "http://127.0.0.1:8317" {
		t.Fatalf("port 0 fallback base url = %q", got)
	}
}

func TestClientConnectWildcardBindUsesLoopback(t *testing.T) {
	for _, bind := range []string{"0.0.0.0", "::"} {
		s, root := listenServer(t, bind, 9124)
		if got := connectClaudeBaseURL(t, s, root); got != "http://127.0.0.1:9124" {
			t.Fatalf("bind %s: base url = %q", bind, got)
		}
	}
}

func TestClientConnectIPv6BindIsBracketed(t *testing.T) {
	s, root := listenServer(t, "::1", 9125)
	if got := connectClaudeBaseURL(t, s, root); got != "http://[::1]:9125" {
		t.Fatalf("base url = %q", got)
	}
}

func TestClientConnectPrefersBoundListener(t *testing.T) {
	s, root := listenServer(t, "127.0.0.1", 1)
	s.setListenAddr("127.0.0.1:45678")
	if got := connectClaudeBaseURL(t, s, root); got != "http://127.0.0.1:45678" {
		t.Fatalf("base url = %q", got)
	}
}

func TestClientConnectRejectsBadBaseURL(t *testing.T) {
	for _, body := range []string{
		`{"baseURL":"http://x\"\nfoo=1"}`,
		`{"baseURL":"ftp://h"}`,
		`{"baseURL":"http://"}`,
	} {
		s, root := listenServer(t, "127.0.0.1", 9127)
		req := httptest.NewRequest(http.MethodPost, "/admin/clients/claude-code/connect", strings.NewReader(body))
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d body %s", body, rr.Code, rr.Body.String())
		}
		if _, err := os.Stat(filepath.Join(root, ".claude", "settings.json")); !os.IsNotExist(err) {
			t.Fatalf("%s: rejected connect wrote settings.json: %v", body, err)
		}
	}
}
