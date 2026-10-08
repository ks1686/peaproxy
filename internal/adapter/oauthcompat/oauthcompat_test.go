package oauthcompat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

func TestOpenTagsOAuthModelsAndSuppressesAPIKeyOnlyFeatures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer oauth-token" {
			t.Errorf("request = %s authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"subscription-model"}]}`)
	}))
	t.Cleanup(srv.Close)

	tagged, err := Open(adapter.Options{ID: "subscription", BaseURL: srv.URL + "/v1", APIKey: "oauth-token"}, "example_oauth")
	if err != nil {
		t.Fatal(err)
	}
	caps := tagged.Capabilities()
	if !caps.OAuth || caps.APIKey || caps.ImageOut || caps.Embeddings || !caps.Chat || !caps.ListModels {
		t.Fatalf("capabilities = %#v", caps)
	}
	models, err := tagged.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %#v", models)
	}
	model := models[0]
	if !model.SubscriptionOAuth || model.Provider != "example_oauth" || model.AccountID != "subscription" || model.Tier != catalog.TierPaid || !model.Exposed || !model.Routable {
		t.Errorf("model = %#v", model)
	}
}

func TestOpenDefaultsAccountToProvider(t *testing.T) {
	tagged, err := Open(adapter.Options{BaseURL: "http://example.test/v1"}, "provider")
	if err != nil {
		t.Fatal(err)
	}
	if tagged.Account != "provider" || tagged.ID() != "openai_compat" {
		t.Errorf("tagged = %#v id=%q", tagged, tagged.ID())
	}
}
