package adapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/anthropic"
	"github.com/ks1686/peaproxy/internal/adapter/anthropic_oauth"
	"github.com/ks1686/peaproxy/internal/adapter/ollama"
	"github.com/ks1686/peaproxy/internal/adapter/openai"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/adapter/openai_oauth"
	"github.com/ks1686/peaproxy/internal/adapter/opencodezen"
	"github.com/ks1686/peaproxy/internal/adapter/openrouter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
)

func TestDefaultRegistryHasStubAndLiveFactories(t *testing.T) {
	r := adapters.DefaultRegistry()
	for _, name := range []string{"ollama", "openai_compat", "openai", "anthropic", "openrouter", "opencode_zen", "anthropic_oauth", "openai_oauth", "lmstudio", "groq", "cerebras", "google", "gemini", "xai", "huggingface", "antigravity", "gemini_oauth", "xai_oauth", "kimi_oauth", "kimi_ai_oauth", "meta_oauth", "qwen_oauth"} {
		if _, err := r.Open(name, adapter.Options{ID: name, BaseURL: "http://127.0.0.1:9/v1"}); err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
	}
}

func TestOllamaListModelsUsesLiveEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []map[string]string{{"id": "llama3.2"}},
		})
	}))
	t.Cleanup(srv.Close)

	a, err := ollama.New(adapter.Options{ID: "ollama-local", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "llama3.2" || models[0].Tier != catalog.TierLocal {
		t.Fatalf("%#v", models)
	}
}

func TestOpenAICompatRequiresBaseURL(t *testing.T) {
	_, err := openai_compat.New(adapter.Options{ID: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestOAuthAdaptersImplementAuthenticator(t *testing.T) {
	ctx := context.Background()
	anth, err := anthropic_oauth.New(adapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anth.ListModels(ctx); !errors.Is(err, adapter.ErrAuthRequired) {
		t.Fatalf("anthropic_oauth without token: %v", err)
	}
	oa, err := openai_oauth.New(adapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oa.Chat(ctx, adapter.ChatRequest{}); !errors.Is(err, adapter.ErrAuthRequired) {
		t.Fatalf("openai_oauth without token: %v", err)
	}
	ag, err := adapters.DefaultRegistry().Open("antigravity", adapter.Options{ID: "ag", SkipLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.ListModels(ctx); !errors.Is(err, adapter.ErrAuthRequired) {
		t.Fatalf("antigravity without token: %v", err)
	}
	if _, ok := ag.(adapter.Authenticator); !ok {
		t.Fatal("antigravity must implement Authenticator")
	}
	if !anth.Capabilities().OAuth || !oa.Capabilities().OAuth || !ag.Capabilities().OAuth {
		t.Fatal("OAuth capability should be advertised")
	}
}

func TestOpenAICompatChatAndStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Contains(raw, []byte(`"model":"m"`)) {
			t.Errorf("body %s", raw)
		}
		if strings.Contains(string(raw), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"chunk\"}}]}\n\n")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}))
	t.Cleanup(srv.Close)
	a, err := openai_compat.New(adapter.Options{ID: "x", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "m",
		Raw:   []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil || resp.Content != "ok" {
		t.Fatalf("%v %#v", err, resp)
	}
	var buf bytes.Buffer
	if err := a.ChatStream(context.Background(), adapter.ChatRequest{
		Model: "m",
		Raw:   []byte(`{"model":"m","messages":[]}`),
	}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "chunk") {
		t.Fatalf("stream %s", buf.String())
	}
}

func TestOllamaFallsBackToTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "llama3.2:latest"}}})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	a, err := ollama.New(adapter.Options{ID: "o", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "llama3.2:latest" {
		t.Fatalf("%#v", models)
	}
}

func TestZenTagsFreeAndPrivacy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "nemotron-3-free"},
				{"id": "some-paid-model"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	a, err := opencodezen.New(adapter.Options{ID: "zen", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("%#v", models)
	}
	if models[0].Tier != catalog.TierFree || models[0].PrivacyNote == "" {
		t.Fatalf("nemotron should be free with privacy note: %#v", models[0])
	}
	if models[1].Tier != catalog.TierPaid {
		t.Fatalf("paid id: %#v", models[1])
	}
}

func TestOpenAIDefaultsBaseURL(t *testing.T) {
	a, err := openai.New(adapter.Options{ID: "oa", APIKey: "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID() != "oa" {
		t.Fatalf("id %s", a.ID())
	}
}

func TestOpenRouterTagsFreeModelsAndHeaders(t *testing.T) {
	var gotRef, gotTitle, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRef = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-Title")
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "meta-llama/llama-3.3-70b-instruct:free"},
				{"id": "openai/gpt-4o"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	a, err := openrouter.New(adapter.Options{ID: "or", BaseURL: srv.URL + "/v1", APIKey: "or-key"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("%#v", models)
	}
	if models[0].Tier != catalog.TierFree || models[0].Provider != "openrouter" {
		t.Fatalf("free tag: %#v", models[0])
	}
	if models[1].Tier != catalog.TierFreemium {
		t.Fatalf("paid/fallback: %#v", models[1])
	}
	if gotRef == "" || gotTitle == "" || gotAuth != "Bearer or-key" {
		t.Fatalf("headers ref=%q title=%q auth=%q", gotRef, gotTitle, gotAuth)
	}
}

func TestAnthropicListModelsAndMessagesBridge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "ak-test" || r.Header.Get("anthropic-version") == "" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v1/models" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "claude-sonnet-4-20250514", "display_name": "Claude Sonnet 4"}},
			})
		case r.URL.Path == "/v1/messages":
			raw, _ := io.ReadAll(r.Body)
			if !bytes.Contains(raw, []byte(`"max_tokens"`)) || bytes.Contains(raw, []byte(`"image_url"`)) {
				t.Errorf("expected Claude body, got %s", raw)
			}
			if strings.Contains(string(raw), `"stream":true`) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-4-20250514\"}}\n\n")
				_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
				_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    "msg_1",
				"model": "claude-sonnet-4-20250514",
				"content": []map[string]string{{
					"type": "text", "text": "hello claude",
				}},
				"stop_reason": "end_turn",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a, err := anthropic.New(adapter.Options{ID: "anth", BaseURL: srv.URL, APIKey: "ak-test"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "claude-sonnet-4-20250514" {
		t.Fatalf("%#v", models)
	}
	if !containsStr(models[0].Modalities, "image_in") {
		t.Fatalf("vision tag: %#v", models[0].Modalities)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "claude-sonnet-4-20250514",
		Raw:   []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil || resp.Content != "hello claude" {
		t.Fatalf("%v %#v", err, resp)
	}
	if !bytes.Contains(resp.Raw, []byte(`"object":"chat.completion"`)) {
		t.Fatalf("openai bridge: %s", resp.Raw)
	}
	nm, ok := a.(adapter.NativeMessages)
	if !ok {
		t.Fatal("anthropic must implement NativeMessages")
	}
	out, err := nm.Messages(context.Background(), []byte(`{"model":"claude-sonnet-4-20250514","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil || !bytes.Contains(out, []byte("hello claude")) {
		t.Fatalf("%v %s", err, out)
	}
	var buf bytes.Buffer
	if err := a.ChatStream(context.Background(), adapter.ChatRequest{
		Model: "claude-sonnet-4-20250514",
		Raw:   []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}`),
	}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"content":"hi"`) || !strings.Contains(buf.String(), "[DONE]") {
		t.Fatalf("openai sse: %s", buf.String())
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
