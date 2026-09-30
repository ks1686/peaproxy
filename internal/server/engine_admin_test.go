package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/clients"
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

func TestClientConnectGuidedSnippetFollowsOrigin(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"listen port", `{"model":"m"}`, "http://127.0.0.1:9128"},
		{"requested baseURL", `{"baseURL":"http://127.0.0.1:9129/v1","model":"m"}`, "http://127.0.0.1:9129"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a server listening off the default port.
			s, _ := listenServer(t, "127.0.0.1", 9128)
			// When OpenCode is guided-connected.
			rr := httptest.NewRecorder()
			s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/clients/opencode/connect", strings.NewReader(tc.body)))
			var parsed struct {
				Status  string `json:"status"`
				Snippet string `json:"snippet"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &parsed); err != nil || parsed.Status != "guided" {
				t.Fatalf("status %d body %s err %v", rr.Code, rr.Body.String(), err)
			}
			// Then the snippet points at that address, not :8317.
			if strings.Contains(parsed.Snippet, ":8317") || !strings.Contains(parsed.Snippet, tc.want) {
				t.Fatalf("guided snippet does not follow %s:\n%s", tc.want, parsed.Snippet)
			}
		})
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

type adminClient struct {
	Name        string `json:"name"`
	BaseURL     string `json:"baseURL"`
	Cloak       string `json:"cloak"`
	Notes       string `json:"notes"`
	Snippet     string `json:"snippet"`
	Verify      string `json:"verify"`
	Connectable bool   `json:"connectable"`
}

func getAdminClients(t *testing.T, s *Server) (string, []adminClient) {
	t.Helper()
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/admin/clients", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var parsed struct {
		Clients []adminClient `json:"clients"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	return rr.Body.String(), parsed.Clients
}

func postVerify(t *testing.T, s *Server, name string) string {
	t.Helper()
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/clients/"+name+"/verify", strings.NewReader(`{}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var parsed struct {
		Verify string `json:"verify"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed.Verify
}

func TestAdminClientsUseListenPort(t *testing.T) {
	s, _ := listenServer(t, "0.0.0.0", 9126)
	_, got := getAdminClients(t, s)
	if len(got) == 0 {
		t.Fatal("no presets")
	}
	for _, c := range got {
		if strings.Contains(c.BaseURL, "8317") || strings.Contains(c.Snippet, "8317") {
			t.Fatalf("%s still points at 8317: %+v", c.Name, c)
		}
		if !strings.Contains(c.BaseURL, "http://127.0.0.1:9126") {
			t.Fatalf("%s baseURL = %q", c.Name, c.BaseURL)
		}
		if !strings.HasSuffix(c.Verify, " --origin http://127.0.0.1:9126") {
			t.Fatalf("%s preset verify = %q", c.Name, c.Verify)
		}
		if want := "peaproxy clients verify " + c.Name + " --origin http://127.0.0.1:9126"; postVerify(t, s, c.Name) != want {
			t.Fatalf("%s verify = %q, want %q", c.Name, postVerify(t, s, c.Name), want)
		}
	}

	s, _ = listenServer(t, "127.0.0.1", 8317)
	body, _ := getAdminClients(t, s)
	var want []adminClient
	for _, n := range clients.List() {
		p, _ := clients.Get(n)
		want = append(want, adminClient{Name: p.Name, BaseURL: p.BaseURL, Cloak: p.Cloak, Notes: p.Notes, Snippet: p.Snippet, Verify: p.Verify, Connectable: clients.Connectable(n)})
	}
	var buf strings.Builder
	if err := json.NewEncoder(&buf).Encode(map[string]any{"clients": want}); err != nil {
		t.Fatal(err)
	}
	if body != buf.String() {
		t.Fatalf("default-port payload changed:\n%s\nwant:\n%s", body, buf.String())
	}
	if got := postVerify(t, s, "claude-code"); got != "peaproxy clients verify claude-code" {
		t.Fatalf("default-port verify = %q", got)
	}
}

// #61: the UI decided which clients it could connect from a list hardcoded in
// app.js, so Pi was missing even though connect works for it. The payload says
// which ones are connectable instead, and the list cannot drift.
func TestAdminClientsSaysWhichAreConnectable(t *testing.T) {
	s, _ := testServer(t)
	s.clientRoot = t.TempDir()
	req := httptest.NewRequest(http.MethodGet, "/admin/clients", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var payload struct {
		Clients []struct {
			Name        string `json:"name"`
			Connectable bool   `json:"connectable"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range payload.Clients {
		got[c.Name] = c.Connectable
	}
	for _, name := range []string{"pi", "opencode", "continue", "codex", "claude-code"} {
		if !got[name] {
			t.Errorf("%s should be reported connectable; payload says %v", name, got)
		}
	}
	for _, name := range []string{"cursor", "cline", "amp"} {
		if got[name] {
			t.Errorf("%s has no managed config and should not be connectable", name)
		}
	}
}
