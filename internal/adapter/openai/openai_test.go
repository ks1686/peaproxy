package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

func TestNewUsesOpenAIIdentityDefaultsAndListsModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("request = %s %s, want GET /v1/models", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"gpt-test"}]}`)
	}))
	t.Cleanup(srv.Close)

	adp, err := New(adapter.Options{ID: "work", BaseURL: srv.URL + "/v1", APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if adp.ID() != "work" {
		t.Fatalf("ID = %q, want work", adp.ID())
	}
	if !adp.Capabilities().APIKey || adp.Capabilities().Local {
		t.Fatalf("capabilities = %#v", adp.Capabilities())
	}

	models, err := adp.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %#v", models)
	}
	model := models[0]
	if model.Provider != Name || model.AccountID != "work" || model.Tier != catalog.TierPaid || !model.Exposed || !model.Routable {
		t.Errorf("model = %#v", model)
	}
}

func TestNewAppliesDefaultBaseURLAndPaidTier(t *testing.T) {
	adp, err := New(adapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if adp.ID() != "openai_compat" {
		t.Errorf("default ID = %q", adp.ID())
	}
	if !adp.Capabilities().Chat || adp.Capabilities().APIKey {
		t.Errorf("capabilities = %#v", adp.Capabilities())
	}
}
