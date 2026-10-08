package ollama

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

func TestListModelsFallsBackToNativeTags(t *testing.T) {
	var compatRequests, tagRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			compatRequests++
			_, _ = io.WriteString(w, `{"data":[]}`)
		case "/api/tags":
			tagRequests++
			_, _ = io.WriteString(w, `{"models":[{"name":"llama3.2:latest"},{"name":"nomic-embed-text"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	adp, err := New(adapter.Options{ID: "local", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if !adp.Capabilities().Local || adp.Capabilities().APIKey {
		t.Fatalf("capabilities = %#v", adp.Capabilities())
	}
	models, err := adp.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if compatRequests != 1 || tagRequests != 1 || len(models) != 2 {
		t.Fatalf("requests compat=%d tags=%d models=%#v", compatRequests, tagRequests, models)
	}
	for _, model := range models {
		if model.Provider != Name || model.AccountID != "local" || model.Tier != catalog.TierLocal || !model.Exposed || !model.Routable {
			t.Errorf("model = %#v", model)
		}
	}
}

func TestListModelsUsesCompatibleEndpointWhenItReturnsModels(t *testing.T) {
	var tagsCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"llama3.2"}]}`)
		case "/api/tags":
			tagsCalled = true
			http.Error(w, "should not be called", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	adp, err := New(adapter.Options{BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := adp.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tagsCalled || len(models) != 1 || models[0].Provider != Name || models[0].Tier != catalog.TierLocal {
		t.Fatalf("tagsCalled=%v models=%#v", tagsCalled, models)
	}
}
