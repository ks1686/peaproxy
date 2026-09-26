package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/anthropic_oauth"
	"github.com/ks1686/peaproxy/internal/adapter/ollama"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/adapter/openai_oauth"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
)

func TestDefaultRegistryHasStubAndLiveFactories(t *testing.T) {
	r := adapters.DefaultRegistry()
	for _, name := range []string{"ollama", "openai_compat", "anthropic_oauth", "openai_oauth"} {
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
