package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/clients"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
)

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data":   []map[string]string{{"id": "llama3.2"}, {"id": "secret-model"}},
			})
		case r.URL.Path == "/v1/chat/completions":
			raw, _ := io.ReadAll(r.Body)
			var req struct {
				Model    string `json:"model"`
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
				Stream bool `json:"stream"`
			}
			_ = json.Unmarshal(raw, &req)
			msg := "hello from " + req.Model
			if req.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"model\":\""+req.Model+"\",\"choices\":[{\"delta\":{\"content\":\""+msg+"\"}}]}\n\ndata: [DONE]\n\n")
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    "chatcmpl-test",
				"model": req.Model,
				"choices": []map[string]any{{
					"message":       map[string]string{"role": "assistant", "content": msg},
					"finish_reason": "stop",
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Hide:          config.HideList{Models: []string{"secret-model"}},
		Providers: []config.Provider{{
			ID:      "local",
			Adapter: "openai_compat",
			Tier:    "local",
			BaseURL: up.URL + "/v1",
		}},
	}
	gw, err := gateway.New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	return New(Options{Gateway: gw}), up
}

func TestHealthReportsLoopbackBind(t *testing.T) {
	s, _ := testServer(t)
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

func TestV1ModelsHidesModelButChatStillRoutes(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	var list catalog.OpenAIModelList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, m := range list.Data {
		if m.ID == "secret-model" {
			t.Fatal("hidden model leaked into /v1/models")
		}
	}
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"secret-model","messages":[{"role":"user","content":"hi"}]}`))
	chat.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr2, chat)
	if rr2.Code != http.StatusOK {
		t.Fatalf("hidden model should still route: %d %s", rr2.Code, rr2.Body.String())
	}
	if !strings.Contains(rr2.Body.String(), "secret-model") {
		t.Fatalf("chat body: %s", rr2.Body)
	}
}

func TestV1ModelsFilterLocal(t *testing.T) {
	s, _ := testServer(t)
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

func TestChatCompletionsNonStream(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"llama3.2","messages":[{"role":"user","content":"hi"}]}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
}

func TestClaudeMessagesTranslates(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"llama3.2","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), `"type":"message"`) {
		t.Fatalf("%s", rr.Body)
	}
}

func TestResponsesTranslatesViaChat(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"llama3.2","input":"hi"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), `"object":"response"`) {
		t.Fatalf("want Responses object, got %s", rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "hello from llama3.2") {
		t.Fatalf("content: %s", rr.Body)
	}
}

func TestShowcaseAndUsage(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/showcase", bytes.NewReader([]byte(`{"model":"llama3.2","prompt":"hi"}`)))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	u := httptest.NewRequest(http.MethodGet, "/admin/usage", nil)
	rr2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr2, u)
	if !strings.Contains(rr2.Body.String(), "showcase") {
		t.Fatalf("usage: %s", rr2.Body)
	}
}

func TestUIServesIndex(t *testing.T) {
	s, _ := testServer(t)
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
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/ui/app.js", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
}

func TestClaudeMessagesTrueSSE(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"llama3.2","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	got := rr.Body.String()
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type %s", ct)
	}
	if strings.Contains(got, "event: message\n") && !strings.Contains(got, "event: message_start") {
		t.Fatalf("single-event wrapper: %s", got)
	}
	for _, ev := range []string{"event: message_start", "event: content_block_delta", "event: message_stop"} {
		if !strings.Contains(got, ev) {
			t.Fatalf("missing %s in %s", ev, got)
		}
	}
}

func TestShowcaseVisionPassesImageURL(t *testing.T) {
	s, _ := testServer(t)
	body := `{"model":"llama3.2","prompt":"what is this","imageUrl":"https://example.com/cat.png"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/showcase", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
}

func TestHealthListsNativeAdaptersAndCooldowns(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/health", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	got := rr.Body.String()
	for _, name := range []string{"anthropic", "openai", "openrouter", "opencode_zen", "lmstudio", "llamacpp", "vllm", "groq", "cerebras", "google", "gemini", "xai", "huggingface", "nim", "workers_ai", "ollama_cloud"} {
		if !strings.Contains(got, name) {
			t.Fatalf("missing adapter %s in %s", name, got)
		}
	}
	if !strings.Contains(got, `"cooldowns"`) {
		t.Fatalf("%s", got)
	}
}

func TestUsageDisclaimerPersisted(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/usage", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if strings.Contains(rr.Body.String(), "In-memory only") {
		t.Fatalf("%s", rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "usage.json") {
		t.Fatalf("%s", rr.Body)
	}
}

func TestDefaultAddr(t *testing.T) {
	cfg := config.Default()
	if cfg.Addr() != "127.0.0.1:8317" {
		t.Fatalf("addr %s", cfg.Addr())
	}
}

func TestHealthzPublicAndPresets(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), `"status":"ok"`) {
		t.Fatalf("%s", rr.Body)
	}
	preq := httptest.NewRequest(http.MethodGet, "/admin/presets", nil)
	prr := httptest.NewRecorder()
	s.Handler().ServeHTTP(prr, preq)
	if prr.Code != http.StatusOK {
		t.Fatalf("presets %d %s", prr.Code, prr.Body)
	}
	for _, name := range []string{"lmstudio", "llamacpp", "vllm", "groq", "google", "huggingface", "nim", "workers_ai", "ollama_cloud", "anthropic_oauth", "openai_oauth", "antigravity", "xai_oauth", "kimi_oauth", "meta_oauth"} {
		if !strings.Contains(prr.Body.String(), name) {
			t.Fatalf("missing %s in %s", name, prr.Body)
		}
	}
}

func TestAdminTokenRequiredOffLoopback(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{}})
	}))
	t.Cleanup(up.Close)
	cfg := config.Config{
		SchemaVersion:    1,
		Bind:             "0.0.0.0",
		Port:             8317,
		AllowNonLoopback: true,
		AdminToken:       "lan-secret",
		Providers: []config.Provider{{
			ID: "local", Adapter: "openai_compat", Tier: "local", BaseURL: up.URL + "/v1",
		}},
	}
	gw, err := gateway.New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Gateway: gw})
	req := httptest.NewRequest(http.MethodGet, "/admin/health", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d %s", rr.Code, rr.Body)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/admin/health", nil)
	req2.Header.Set("X-Admin-Token", "lan-secret")
	rr2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("token should pass: %d %s", rr2.Code, rr2.Body)
	}
	hz := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr3 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr3, hz)
	if rr3.Code != http.StatusOK || !strings.Contains(rr3.Body.String(), `"lan":true`) {
		t.Fatalf("healthz: %d %s", rr3.Code, rr3.Body)
	}
}

func TestUIIncludesToastsAndLanBanner(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	body := rr.Body.String()
	for _, want := range []string{`id="toasts"`, `id="lan-banner"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	js := httptest.NewRequest(http.MethodGet, "/ui/app.js", nil)
	jrr := httptest.NewRecorder()
	s.Handler().ServeHTTP(jrr, js)
	jsBody := jrr.Body.String()
	for _, want := range []string{"peaproxy.catalogFilter", "toast(", "/admin/presets", "/admin/oauth/start", "not liable", "isOAuthAdapter"} {
		if !strings.Contains(jsBody, want) {
			t.Fatalf("app.js missing %s", want)
		}
	}
}

func TestClientsVerifyAgainstLocalAdapter(t *testing.T) {
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	ctx := context.Background()
	for _, name := range []string{"cursor", "opencode", "claude-code", "pi"} {
		res, err := clients.Verify(ctx, name, srv.URL, true)
		if err != nil {
			t.Fatalf("%s: %v detail=%s", name, err, res.Detail)
		}
		if res.ModelsN != 1 || !res.ChatOK {
			t.Fatalf("%s: %#v", name, res)
		}
	}
	pi, err := clients.Verify(ctx, "pi", srv.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pi.Wires) != 2 || pi.Wires[0].Wire != clients.WireChat || pi.Wires[1].Wire != clients.WireMessages {
		t.Fatalf("pi wires %#v", pi.Wires)
	}
	codex, err := clients.Verify(ctx, "codex", srv.URL, true)
	if err != nil {
		t.Fatalf("codex: %v %s", err, codex.Detail)
	}
	if len(codex.Wires) != 1 || codex.Wires[0].Wire != clients.WireResponses || !codex.ChatOK {
		t.Fatalf("codex %#v", codex)
	}
}
