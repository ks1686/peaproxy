package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestResponsesToolsRoundTripHTTP(t *testing.T) {
	var chatBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "llama3.2"}}})
		case "/v1/chat/completions":
			chatBody, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    "chatcmpl-tools",
				"model": "llama3.2",
				"choices": []map[string]any{{
					"message": map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{{
							"id":   "call_1",
							"type": "function",
							"function": map[string]string{
								"name":      "lookup",
								"arguments": `{"q":"x"}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
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
		Providers: []config.Provider{{
			ID: "local", Adapter: "openai_compat", Tier: "local", BaseURL: up.URL + "/v1",
		}},
	}
	gw, err := gateway.New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	s := New(Options{Gateway: gw})
	body := `{"model":"llama3.2","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":[{"role":"user","content":[{"type":"input_text","text":"look"}]},{"type":"function_call_output","call_id":"call_prev","output":"old"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if !strings.Contains(string(chatBody), `"tools"`) || !strings.Contains(string(chatBody), `"role":"tool"`) {
		t.Fatalf("chat upstream %s", chatBody)
	}
	if !strings.Contains(rr.Body.String(), `"type":"function_call"`) || !strings.Contains(rr.Body.String(), `"call_id":"call_1"`) {
		t.Fatalf("responses %s", rr.Body)
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

func TestShowcaseImageOutGeneratesViaProxy(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "dall-e-3"}, {"id": "llama3.2"}},
			})
		case "/v1/images/generations":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"created": 1,
				"data":    []map[string]string{{"url": "https://img.example/cat.png"}},
			})
		case "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "hello from chat"}}},
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
		Providers: []config.Provider{{
			ID: "oa", Adapter: "openai_compat", Tier: "paid", BaseURL: up.URL + "/v1", APIKey: "sk-test",
		}},
	}
	gw, err := gateway.New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	s := New(Options{Gateway: gw})

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"dall-e-3","prompt":"a cat"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("generations %d %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "https://img.example/cat.png") {
		t.Fatalf("generations body %s", rr.Body)
	}

	show := httptest.NewRequest(http.MethodPost, "/admin/showcase", strings.NewReader(`{"model":"dall-e-3","prompt":"a cat","generateImage":true}`))
	srr := httptest.NewRecorder()
	s.Handler().ServeHTTP(srr, show)
	if srr.Code != http.StatusOK {
		t.Fatalf("showcase %d %s", srr.Code, srr.Body)
	}
	if !strings.Contains(srr.Body.String(), `"imageOut":true`) {
		t.Fatalf("showcase %s", srr.Body)
	}
	if strings.Contains(srr.Body.String(), "hello from chat") {
		t.Fatal("must not fake image-out as chat")
	}

	refuse := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"llama3.2","prompt":"a cat"}`))
	frr := httptest.NewRecorder()
	s.Handler().ServeHTTP(frr, refuse)
	if frr.Code != http.StatusBadRequest {
		t.Fatalf("chat-only model status %d %s", frr.Code, frr.Body)
	}
	if strings.Contains(frr.Body.String(), "hello from") {
		t.Fatal("must not fake image-out as chat")
	}

	cat := httptest.NewRequest(http.MethodGet, "/admin/catalog?filter=all", nil)
	crr := httptest.NewRecorder()
	s.Handler().ServeHTTP(crr, cat)
	if !strings.Contains(crr.Body.String(), `"imageGeneration":"proxy"`) {
		t.Fatalf("catalog: %s", crr.Body)
	}
	if !strings.Contains(crr.Body.String(), `"imageOutReady":true`) {
		t.Fatalf("dall-e-3 should be imageOutReady: %s", crr.Body)
	}
}

func TestEmbeddingsProxiesTaggedModelAndRefusesChatOnly(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "text-embedding-3-small"}, {"id": "llama3.2"}},
			})
		case "/v1/embeddings":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"model":  "text-embedding-3-small",
				"data": []map[string]any{{
					"object":    "embedding",
					"index":     0,
					"embedding": []float64{0.1, 0.2, 0.3},
				}},
			})
		case "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "hello from chat"}}},
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
		Providers: []config.Provider{{
			ID: "oa", Adapter: "openai_compat", Tier: "paid", BaseURL: up.URL + "/v1", APIKey: "sk-test",
		}},
	}
	gw, err := gateway.New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	s := New(Options{Gateway: gw})

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"text-embedding-3-small","input":"hello pea"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("embeddings %d %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), `"embedding"`) || !strings.Contains(rr.Body.String(), "0.1") {
		t.Fatalf("embeddings body %s", rr.Body)
	}

	show := httptest.NewRequest(http.MethodPost, "/admin/showcase", strings.NewReader(`{"model":"text-embedding-3-small","prompt":"hello pea","createEmbeddings":true}`))
	srr := httptest.NewRecorder()
	s.Handler().ServeHTTP(srr, show)
	if srr.Code != http.StatusOK {
		t.Fatalf("showcase %d %s", srr.Code, srr.Body)
	}
	if !strings.Contains(srr.Body.String(), `"embeddings":true`) {
		t.Fatalf("showcase %s", srr.Body)
	}
	if strings.Contains(srr.Body.String(), "hello from chat") {
		t.Fatal("must not fake embeddings as chat")
	}

	refuse := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"llama3.2","input":"hello"}`))
	frr := httptest.NewRecorder()
	s.Handler().ServeHTTP(frr, refuse)
	if frr.Code != http.StatusBadRequest {
		t.Fatalf("chat-only model status %d %s", frr.Code, frr.Body)
	}
	if strings.Contains(frr.Body.String(), "hello from") {
		t.Fatal("must not fake embeddings as chat")
	}

	cat := httptest.NewRequest(http.MethodGet, "/admin/catalog?filter=all", nil)
	crr := httptest.NewRecorder()
	s.Handler().ServeHTTP(crr, cat)
	if !strings.Contains(crr.Body.String(), `"embeddings":"proxy"`) {
		t.Fatalf("catalog: %s", crr.Body)
	}
	if !strings.Contains(crr.Body.String(), `"embeddingsReady":true`) {
		t.Fatalf("text-embedding-3-small should be embeddingsReady: %s", crr.Body)
	}
}

func TestHealthListsNativeAdaptersAndCooldowns(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/health", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	got := rr.Body.String()
	for _, name := range []string{"anthropic", "openai", "openrouter", "opencode_zen", "opencode_go", "lmstudio", "llamacpp", "vllm", "groq", "cerebras", "google", "gemini", "xai", "huggingface", "nim", "workers_ai", "ollama_cloud"} {
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
	for _, name := range []string{"lmstudio", "llamacpp", "vllm", "jan", "gpt4all", "groq", "google", "huggingface", "nim", "workers_ai", "ollama_cloud", "sambanova", "anthropic_oauth", "openai_oauth", "antigravity", "xai_oauth", "kimi_oauth", "meta_oauth", "copilot_oauth", "factory_oauth", "opencode_go"} {
		if !strings.Contains(prr.Body.String(), name) {
			t.Fatalf("missing %s in %s", name, prr.Body)
		}
	}
}

func TestAddWorkersAIFillsAccountIDFromEnv(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "cf_acct_from_env")
	s, _ := testServer(t)
	body := `{"id":"workers-ai","adapter":"workers_ai","tier":"freemium","baseURL":"https://api.cloudflare.com/client/v4/accounts/YOUR_ACCOUNT_ID/ai/v1"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("add %d %s", rr.Code, rr.Body)
	}
	list := httptest.NewRequest(http.MethodGet, "/admin/accounts", nil)
	lrr := httptest.NewRecorder()
	s.Handler().ServeHTTP(lrr, list)
	if !strings.Contains(lrr.Body.String(), "cf_acct_from_env") {
		t.Fatalf("expected filled account id in %s", lrr.Body)
	}
	if strings.Contains(lrr.Body.String(), "YOUR_ACCOUNT_ID") {
		t.Fatal("placeholder should be gone after add")
	}
}

func TestPresetsJSONDoesNotLeakEnvValues(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "leak-me-please")
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/presets", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if strings.Contains(rr.Body.String(), "leak-me-please") {
		t.Fatal("preset JSON leaked an env value")
	}
	if !strings.Contains(rr.Body.String(), `"envKey":"GROQ_API_KEY"`) || !strings.Contains(rr.Body.String(), `"envKeySet":true`) {
		t.Fatalf("expected env-key discovery flags: %s", rr.Body)
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
	for _, want := range []string{`id="toasts"`, `id="lan-banner"`, `data-page="requests"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	js := httptest.NewRequest(http.MethodGet, "/ui/app.js", nil)
	jrr := httptest.NewRecorder()
	s.Handler().ServeHTTP(jrr, js)
	jsBody := jrr.Body.String()
	for _, want := range []string{"peaproxy.catalogFilter", "toast(", "/admin/presets", "/admin/oauth/start", "not liable", "isOAuthAdapter", "image_out", "Generate image", "/v1/images/generations", "embeddings", "Embed", "/v1/embeddings", "/admin/requests", "data-pin", "displayName", "envKeySet", "accountIDEnv", "acc-account-id", "secretBackend", "Getting started", "byProvider", "clients verify", "optgroup", "copilot-oauth", "opencode-go", "copilot_oauth", "not reported by provider", "/admin/quota", "quotaHint", "Quota remaining", "quotaByAccount"} {
		if !strings.Contains(jsBody, want) {
			t.Fatalf("app.js missing %s", want)
		}
	}
	css := httptest.NewRequest(http.MethodGet, "/ui/styles.css", nil)
	crr := httptest.NewRecorder()
	s.Handler().ServeHTTP(crr, css)
	if !strings.Contains(crr.Body.String(), "label.row") {
		t.Fatal("styles.css missing label.row (checkbox labels must stay inline)")
	}
}

func TestAdminSettingsReportsPathBackendAndRequestLogWithoutSecrets(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/peaproxy.yaml"
	s, _ := testServer(t)
	s.gw.SetConfigPath(path)
	req := httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["configPath"] != path {
		t.Fatalf("configPath: %#v", body["configPath"])
	}
	if body["secretBackend"] != "file" {
		t.Fatalf("secretBackend: %#v", body["secretBackend"])
	}
	if body["loopback"] != true {
		t.Fatalf("loopback: %#v", body["loopback"])
	}
	if _, ok := body["adminToken"]; ok {
		t.Fatal("admin token must not appear in settings JSON")
	}
	raw := rr.Body.String()
	for _, secret := range []string{"sk-", "accessToken", "refreshToken", "adminToken"} {
		if strings.Contains(raw, secret) && secret != "adminToken" {
			t.Fatalf("settings leaked %s: %s", secret, raw)
		}
	}
	if strings.Contains(raw, `"adminToken"`) {
		t.Fatalf("adminToken field leaked: %s", raw)
	}
	post := httptest.NewRequest(http.MethodPost, "/admin/settings", strings.NewReader(`{"requestLog":true}`))
	prr := httptest.NewRecorder()
	s.Handler().ServeHTTP(prr, post)
	if prr.Code != http.StatusOK {
		t.Fatalf("post %d %s", prr.Code, prr.Body)
	}
	var after map[string]any
	if err := json.Unmarshal(prr.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after["requestLog"] != true {
		t.Fatalf("requestLog not synced: %#v", after)
	}
	logPath, _ := after["requestLogPath"].(string)
	if !strings.HasSuffix(logPath, "requests.log") {
		t.Fatalf("requestLogPath: %#v", after["requestLogPath"])
	}
	if _, ok := after["catalog"].(map[string]any)["Pin"]; ok {
		t.Fatalf("catalog JSON should use yaml-aligned lowercase keys: %#v", after["catalog"])
	}
}

func TestAdminClientsIncludeVerifyCommand(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/clients", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), `"verify":"peaproxy clients verify cursor --chat"`) {
		t.Fatalf("%s", rr.Body)
	}
}

func TestAdminAccountsOnboardingFlag(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/accounts", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if strings.Contains(rr.Body.String(), `"onboarding":true`) {
		t.Fatalf("test server has a configured compat account, should not onboard: %s", rr.Body)
	}
	cfg := config.Default()
	gw, err := gateway.New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	empty := New(Options{Gateway: gw})
	req2 := httptest.NewRequest(http.MethodGet, "/admin/accounts", nil)
	rr2 := httptest.NewRecorder()
	empty.Handler().ServeHTTP(rr2, req2)
	if !strings.Contains(rr2.Body.String(), `"onboarding":true`) {
		t.Fatalf("%s", rr2.Body)
	}
}

func TestAdminUsageIncludesByProvider(t *testing.T) {
	s, _ := testServer(t)
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"llama3.2","messages":[{"role":"user","content":"hi"}]}`))
	crr := httptest.NewRecorder()
	s.Handler().ServeHTTP(crr, chat)
	if crr.Code != http.StatusOK {
		t.Fatalf("chat %d %s", crr.Code, crr.Body)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/usage", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), `"byProvider"`) {
		t.Fatalf("%s", rr.Body)
	}
	if !strings.Contains(rr.Body.String(), `"provider":"openai_compat"`) && !strings.Contains(rr.Body.String(), `"provider":"local"`) {
		// account adapter is openai_compat in testServer
		if !strings.Contains(rr.Body.String(), "openai_compat") {
			t.Fatalf("expected provider on usage: %s", rr.Body)
		}
	}
}

func TestAdminRequestLogDisabledByDefault(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/requests", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), `"enabled":false`) {
		t.Fatalf("%s", rr.Body)
	}
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"llama3.2","messages":[{"role":"user","content":"Authorization: Bearer sk-secret-live"}]}`))
	crr := httptest.NewRecorder()
	s.Handler().ServeHTTP(crr, chat)
	req2 := httptest.NewRequest(http.MethodGet, "/admin/requests", nil)
	rr2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr2, req2)
	if strings.Contains(rr2.Body.String(), "sk-secret-live") {
		t.Fatalf("secret leaked: %s", rr2.Body)
	}
}

func TestAdminRequestLogOptInTailsRedactedEvents(t *testing.T) {
	dir := t.TempDir()
	s, _ := testServer(t)
	s.gw.SetConfigPath(dir + "/peaproxy.yaml")
	on := true
	body, _ := json.Marshal(map[string]any{"requestLog": on})
	post := httptest.NewRequest(http.MethodPost, "/admin/settings", bytes.NewReader(body))
	prr := httptest.NewRecorder()
	s.Handler().ServeHTTP(prr, post)
	if prr.Code != http.StatusOK {
		t.Fatalf("settings %d %s", prr.Code, prr.Body)
	}
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"llama3.2","messages":[{"role":"user","content":"hi Bearer sk-secret-live"}]}`))
	crr := httptest.NewRecorder()
	s.Handler().ServeHTTP(crr, chat)
	if crr.Code != http.StatusOK {
		t.Fatalf("chat %d %s", crr.Code, crr.Body)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/requests", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	got := rr.Body.String()
	if !strings.Contains(got, `"enabled":true`) || !strings.Contains(got, "llama3.2") {
		t.Fatalf("%s", got)
	}
	if strings.Contains(got, "sk-secret-live") {
		t.Fatalf("secret leaked: %s", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("expected redaction: %s", got)
	}
}

func TestAdminRequestLogSurfacesQuotaHintFromHeaders(t *testing.T) {
	dir := t.TempDir()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data":   []map[string]string{{"id": "llama3.2"}},
			})
		case "/v1/chat/completions":
			w.Header().Set("x-ratelimit-remaining-requests", "7")
			w.Header().Set("x-ratelimit-remaining-tokens", "1200")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    "chatcmpl-test",
				"model": "llama3.2",
				"choices": []map[string]any{{
					"message":       map[string]string{"role": "assistant", "content": "ok"},
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
		RequestLog:    true,
		Providers: []config.Provider{{
			ID: "local", Adapter: "openai_compat", Tier: "local", BaseURL: up.URL + "/v1",
		}},
	}
	gw, err := gateway.New(cfg, dir+"/peaproxy.yaml", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	s := New(Options{Gateway: gw})
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"llama3.2","messages":[{"role":"user","content":"hi"}]}`))
	crr := httptest.NewRecorder()
	s.Handler().ServeHTTP(crr, chat)
	if crr.Code != http.StatusOK {
		t.Fatalf("chat %d %s", crr.Code, crr.Body)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/requests", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	got := rr.Body.String()
	if !strings.Contains(got, `"quotaHint":"req=7 tok=1200"`) {
		t.Fatalf("missing remaining hint: %s", got)
	}
}

func TestAdminRequestLogOmitsQuotaHintWhenHeadersMissing(t *testing.T) {
	dir := t.TempDir()
	s, _ := testServer(t)
	s.gw.SetConfigPath(dir + "/peaproxy.yaml")
	on := true
	body, _ := json.Marshal(map[string]any{"requestLog": on})
	post := httptest.NewRequest(http.MethodPost, "/admin/settings", bytes.NewReader(body))
	prr := httptest.NewRecorder()
	s.Handler().ServeHTTP(prr, post)
	if prr.Code != http.StatusOK {
		t.Fatalf("settings %d %s", prr.Code, prr.Body)
	}
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"llama3.2","messages":[{"role":"user","content":"hi"}]}`))
	crr := httptest.NewRecorder()
	s.Handler().ServeHTTP(crr, chat)
	if crr.Code != http.StatusOK {
		t.Fatalf("chat %d %s", crr.Code, crr.Body)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/requests", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if strings.Contains(rr.Body.String(), `"quotaHint"`) {
		t.Fatalf("must not invent remaining: %s", rr.Body)
	}
}

func TestCatalogOverlayPinRenamePersistsAndHidesUnchanged(t *testing.T) {
	dir := t.TempDir()
	s, _ := testServer(t)
	s.gw.SetConfigPath(dir + "/config.yaml")
	body := `{"id":"llama3.2","displayName":"Llama local","pinned":true}`
	req := httptest.NewRequest(http.MethodPost, "/admin/catalog/overlay", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	cat := httptest.NewRequest(http.MethodGet, "/admin/catalog?filter=all", nil)
	crr := httptest.NewRecorder()
	s.Handler().ServeHTTP(crr, cat)
	var payload struct {
		Models []catalog.Model `json:"models"`
	}
	if err := json.Unmarshal(crr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Models) == 0 || payload.Models[0].ID != "llama3.2" || !payload.Models[0].Pinned || payload.Models[0].DisplayName != "Llama local" {
		t.Fatalf("%#v", payload.Models)
	}
	models := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	mrr := httptest.NewRecorder()
	s.Handler().ServeHTTP(mrr, models)
	var list catalog.OpenAIModelList
	if err := json.Unmarshal(mrr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data) == 0 || list.Data[0].ID != "llama3.2" {
		t.Fatalf("pin should order /v1/models by live id: %#v", list.Data)
	}
	onDisk, err := os.ReadFile(dir + "/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), "Llama local") {
		t.Fatalf("overlay not persisted: %s", onDisk)
	}
}

func TestHealthIncludesAdapterHealthAndProbe(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/health", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	got := rr.Body.String()
	if !strings.Contains(got, `"adapterHealth"`) || !strings.Contains(got, `"cooldownTtlMs"`) {
		t.Fatalf("%s", got)
	}
	if !strings.Contains(got, `"quota"`) {
		t.Fatalf("missing quota: %s", got)
	}
	if !strings.Contains(got, `"failoverPolicy":"round-robin"`) {
		t.Fatalf("missing failover policy: %s", got)
	}
	if !strings.Contains(got, `"accountId":"local"`) {
		t.Fatalf("adapter health missing account: %s", got)
	}
	probe := httptest.NewRequest(http.MethodPost, "/admin/health/probe", nil)
	prr := httptest.NewRecorder()
	s.Handler().ServeHTTP(prr, probe)
	if prr.Code != http.StatusOK || !strings.Contains(prr.Body.String(), `"adapterHealth"`) {
		t.Fatalf("probe %d %s", prr.Code, prr.Body)
	}
	if !strings.Contains(prr.Body.String(), `"quota"`) {
		t.Fatalf("probe missing quota: %s", prr.Body)
	}
}

func TestAdminQuotaOmitsUnknownRemaining(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/quota", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	var body struct {
		Honesty  string           `json:"honesty"`
		Quota    []map[string]any `json:"quota"`
		Families []map[string]any `json:"families"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Honesty, "omitted") {
		t.Fatalf("honesty %s", body.Honesty)
	}
	if len(body.Quota) != 1 || body.Quota[0]["accountId"] != "local" {
		t.Fatalf("%#v", body.Quota)
	}
	if _, ok := body.Quota[0]["remainingRequests"]; ok {
		t.Fatalf("unknown remaining leaked: %#v", body.Quota[0])
	}
	if _, ok := body.Quota[0]["creditsUnlimited"]; ok {
		t.Fatalf("must not invent unlimited: %#v", body.Quota[0])
	}
	var sawOpenRouter bool
	for _, f := range body.Families {
		if f["adapter"] == "openrouter" {
			sawOpenRouter = true
			if f["probe"] != "GET /api/v1/key" {
				t.Fatalf("%#v", f)
			}
		}
		if f["adapter"] == "copilot_oauth" {
			if _, ok := f["probe"]; ok && f["probe"] != "" && f["probe"] != nil {
				t.Fatalf("copilot must not probe: %#v", f)
			}
		}
	}
	if !sawOpenRouter {
		t.Fatal("families missing openrouter")
	}
}

func TestClientsVerifyAgainstLocalAdapter(t *testing.T) {
	s, _ := testServer(t)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	ctx := context.Background()
	for _, name := range []string{"cursor", "opencode", "claude-code", "pi", "amp", "continue", "cline", "droid"} {
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
	cli := httptest.NewRequest(http.MethodGet, "/admin/clients", nil)
	crr := httptest.NewRecorder()
	s.Handler().ServeHTTP(crr, cli)
	for _, name := range []string{"amp", "continue", "cline", "pi", "codex"} {
		if !strings.Contains(crr.Body.String(), `"name":"`+name+`"`) {
			t.Fatalf("admin clients missing %s: %s", name, crr.Body)
		}
	}
}

func TestMalformedChatJSONIs400(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("not-json"))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "invalid character") {
		t.Fatalf("body %s", rr.Body)
	}
}

func TestFaviconIsServed(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type %q", ct)
	}
	if rr.Body.Len() == 0 {
		t.Fatal("empty favicon")
	}
}
