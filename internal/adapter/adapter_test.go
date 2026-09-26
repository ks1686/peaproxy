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
	"github.com/ks1686/peaproxy/internal/adapter/anthropic_oauth"
	"github.com/ks1686/peaproxy/internal/adapter/ollama"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/adapter/openai_oauth"
	"github.com/ks1686/peaproxy/internal/adapter/opencodezen"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
)

func TestDefaultRegistryHasStubAndLiveFactories(t *testing.T) {
	r := adapters.DefaultRegistry()
	for _, name := range []string{"ollama", "openai_compat", "opencode_zen", "anthropic_oauth", "openai_oauth"} {
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

func TestOAuthStubsAreNotImplemented(t *testing.T) {
	ctx := context.Background()
	anth, err := anthropic_oauth.New(adapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anth.ListModels(ctx); !errors.Is(err, adapter.ErrNotImplemented) {
		t.Fatalf("anthropic_oauth: %v", err)
	}
	oa, err := openai_oauth.New(adapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oa.Chat(ctx, adapter.ChatRequest{}); !errors.Is(err, adapter.ErrNotImplemented) {
		t.Fatalf("openai_oauth: %v", err)
	}
	auth, ok := anth.(adapter.Authenticator)
	if !ok {
		t.Fatal("anthropic_oauth must implement Authenticator")
	}
	if _, err := auth.AuthStart(ctx); !errors.Is(err, adapter.ErrNotImplemented) {
		t.Fatalf("AuthStart: %v", err)
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
