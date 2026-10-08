package openrouter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

func TestNewSuppliesOpenRouterHeadersAndTagsFreeModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("HTTP-Referer"); got != "https://github.com/ks1686/peaproxy" {
			t.Errorf("referer = %q", got)
		}
		if got := r.Header.Get("X-Title"); got != "PeaProxy" {
			t.Errorf("title = %q", got)
		}
		if got := r.Header.Get("X-Custom"); got != "kept" {
			t.Errorf("custom header = %q", got)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"vendor/free-model:free"}]}`)
	}))
	t.Cleanup(srv.Close)

	adp, err := New(adapter.Options{ID: "router-account", BaseURL: srv.URL + "/v1", ExtraHeaders: map[string]string{"X-Custom": "kept"}})
	if err != nil {
		t.Fatal(err)
	}
	models, err := adp.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Provider != Name || models[0].AccountID != "router-account" || models[0].Tier != catalog.TierFree {
		t.Fatalf("models = %#v", models)
	}
}

func TestNewDoesNotReplaceCallerOpenRouterHeaders(t *testing.T) {
	headers := map[string]string{"HTTP-Referer": "https://example.test", "X-Title": "Caller"}
	if _, err := New(adapter.Options{ExtraHeaders: headers}); err != nil {
		t.Fatal(err)
	}
	if headers["HTTP-Referer"] != "https://example.test" || headers["X-Title"] != "Caller" {
		t.Errorf("caller headers were overwritten: %#v", headers)
	}
}
