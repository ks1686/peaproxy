package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
)

func testServer() *Server {
	cfg := config.Default()
	cfg.Hide.Providers = []string{"hidden-vendor"}
	return New(Options{
		Config: cfg,
		Models: []catalog.Model{
			{ID: "llama3.2", Provider: "ollama", Tier: catalog.TierLocal},
			{ID: "secret", Provider: "hidden-vendor", Tier: catalog.TierPaid},
			{ID: "gpt-x", Provider: "openai", Tier: catalog.TierPaid},
		},
	})
}

func TestHealthReportsLoopbackBind(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest(http.MethodGet, "/admin/health", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["bind"] != "127.0.0.1" || body["port"].(float64) != 8317 {
		t.Fatalf("%v", body)
	}
}

func TestV1ModelsHidesProvider(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var list catalog.OpenAIModelList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, m := range list.Data {
		if m.ID == "secret" {
			t.Fatal("hidden provider leaked into /v1/models")
		}
	}
}

func TestV1ModelsFilterLocal(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest(http.MethodGet, "/v1/models?filter=local", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	var list catalog.OpenAIModelList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != "llama3.2" {
		t.Fatalf("%#v", list.Data)
	}
}

func TestUIServesIndex(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if got := rr.Body.String(); len(got) < 20 {
		t.Fatalf("empty ui: %q", got)
	}
}

func TestUIServesStaticJS(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest(http.MethodGet, "/ui/app.js", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
}

func TestClaudeMessagesIsStub(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status %d", rr.Code)
	}
}

func TestDefaultAddr(t *testing.T) {
	cfg := config.Default()
	if cfg.Addr() != "127.0.0.1:8317" {
		t.Fatalf("addr %s", cfg.Addr())
	}
}
