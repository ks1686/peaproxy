package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
	"github.com/ks1686/peaproxy/internal/usage"
)

// The point of the error store is that a failure leaves something behind that
// says what it was. These go through the real server path rather than calling
// the classifier directly, because the part that can silently not happen is the
// wiring: a failure classified correctly and then never written.

type failingUpstream struct {
	status int
	body   string
}

func (f failingUpstream) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]string{{"id": "m"}}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// errorServer writes a real config so the gateway resolves the store paths the
// way it does in production, rather than having the test hand-pick them.
func errorServer(t *testing.T, cfgYAML string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	gw, err := gateway.New(cfg, cfgPath, adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	// The catalog is what makes a model routable; without it every request is
	// refused as "no account available" and nothing reaches the upstream.
	gw.Refresh(context.Background())
	return New(Options{Gateway: gw}), usage.ErrorLogPath(dir)
}

func providerConfig(t *testing.T, body string) string {
	t.Helper()
	srv := failingUpstream{status: 401, body: body}.server(t)
	return "schemaVersion: 1\nbind: 127.0.0.1\nport: 8317\nrequestLog: true\nproviders:\n" +
		"  - id: acct\n    adapter: openai_compat\n    tier: paid\n    baseURL: " + srv.URL + "/v1\n"
}

func postFailingChat(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"my key is ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"}]}`))
	req.RemoteAddr = "127.0.0.1:4321"
	req.Host = "127.0.0.1:8317"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

func TestAFailedRequestLandsInTheErrorStoreWithItsClass(t *testing.T) {
	s, path := errorServer(t, providerConfig(t, `{"error":{"message":"Incorrect API key provided: ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"}}`))

	rr := postFailingChat(t, s)
	if rr.Code < 400 {
		t.Fatalf("want a failure through, got %d: %s", rr.Code, rr.Body)
	}

	recs, err := usage.ReadErrorRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("wrote %d records, want 1: %+v", len(recs), recs)
	}
	if recs[0].Class != usage.ClassAuthRevoked {
		t.Fatalf("class = %q, want %q", recs[0].Class, usage.ClassAuthRevoked)
	}
	if recs[0].AccountID != "acct" {
		t.Fatalf("accountId = %q, want acct", recs[0].AccountID)
	}
	if recs[0].Time.IsZero() {
		t.Fatal("record has no time, so time-based retention cannot judge it")
	}
}

// This is the whole reason the file is separate: it is going to be uploaded, and
// an upstream 401 quotes the credential that failed.
func TestTheErrorStoreNeverCarriesTheFailingCredential(t *testing.T) {
	s, path := errorServer(t, providerConfig(t, `{"error":{"message":"Incorrect API key provided: ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"}}`))
	postFailingChat(t, s)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
		t.Fatalf("the failing key reached a file meant to be uploaded: %s", b)
	}
	if !strings.Contains(string(b), "auth_revoked") {
		t.Fatalf("redaction destroyed the class: %s", b)
	}
}

// And the prompt goes nowhere, for the same reason.
func TestTheErrorStoreNeverCarriesThePrompt(t *testing.T) {
	s, path := errorServer(t, providerConfig(t, `{"error":"boom"}`))
	postFailingChat(t, s)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "my key is") {
		t.Fatalf("the error store carried the prompt: %s", b)
	}
}

// A success must not appear at all, or the file fills with 200s and stops being
// the thing you open when something breaks.
func TestASuccessfulRequestIsNotRecorded(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]string{{"id": "m"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "c", "object": "chat.completion", "model": "m",
			"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": "hi"}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 3, "completion_tokens": 2},
		})
	}))
	t.Cleanup(ok.Close)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	y := "schemaVersion: 1\nbind: 127.0.0.1\nport: 8317\nrequestLog: true\nproviders:\n" +
		"  - id: acct\n    adapter: openai_compat\n    tier: paid\n    baseURL: " + ok.URL + "/v1\n"
	if err := os.WriteFile(cfgPath, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	gw, err := gateway.New(cfg, cfgPath, adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	if rr := postFailingChat(t, New(Options{Gateway: gw})); rr.Code != http.StatusOK {
		t.Fatalf("setup request failed: %d %s", rr.Code, rr.Body)
	}

	if _, err := os.Stat(usage.ErrorLogPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("a successful request created an error record: %v", err)
	}
}
