package clients

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #134: Pi's built-ins only know Anthropic and OpenAI models. Live models that
// Pi does not know must be importable into a PeaProxy-owned provider, and that
// import must be idempotent and removable without touching user data.

func importModels(t *testing.T, raw string, account, baseURL string, models []PiImportModel) string {
	t.Helper()
	out, err := importPi(a(raw), account, baseURL, models)
	if err != nil {
		t.Fatalf("importPi: %v", err)
	}
	return string(out)
}

func a(s string) []byte { return []byte(s) }

func TestPiImportAddsOwnedAccountProvider(t *testing.T) {
	models := []PiImportModel{
		{ID: "gemini-3.8-flash", AccountID: "work", Input: []string{"text", "image"}, ContextWindow: 1048576},
		{ID: "claude-sonnet-5", AccountID: "work"}, // built-in Anthropic: must be skipped
		{ID: "gpt-5.5", AccountID: "work"},         // built-in OpenAI: must be skipped
		{ID: "gemini-3.8-pro", AccountID: "other"}, // must not leak across accounts
	}
	got := importModels(t, `{}`, "work", "http://127.0.0.1:8317", models)
	var doc struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			API     string `json:"api"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatal(err)
	}
	p, ok := doc.Providers["peaproxy-work"]
	if !ok {
		t.Fatalf("owned provider missing: %s", got)
	}
	if p.BaseURL != "http://127.0.0.1:8317/v1" || p.API != "openai-completions" || p.APIKey != ownedAPIKey {
		t.Fatalf("provider = %+v", p)
	}
	if len(p.Models) != 1 || p.Models[0].ID != "gemini-3.8-flash" {
		t.Fatalf("models = %+v; Claude and GPT must not be imported", p.Models)
	}
}

func TestPiImportFiltersOtherAccounts(t *testing.T) {
	models := []PiImportModel{{ID: "gemini-work", AccountID: "work"}, {ID: "gemini-other", AccountID: "other"}}
	got := importModels(t, `{}`, "work", "http://127.0.0.1:8317", models)
	if !strings.Contains(got, "gemini-work") || strings.Contains(got, "gemini-other") {
		t.Fatalf("imported models from wrong accounts: %s", got)
	}
}

func TestPiImportIsIdempotent(t *testing.T) {
	models := []PiImportModel{{ID: "gemini-3.8-flash", AccountID: "work"}}
	once := importModels(t, `{}`, "work", "http://127.0.0.1:8317", models)
	twice := importModels(t, once, "work", "http://127.0.0.1:8317", models)
	if once != twice {
		t.Fatalf("second import changed the file:\n%s\n---\n%s", once, twice)
	}
}

func TestPiImportKeepsUserProviders(t *testing.T) {
	raw := `{"providers":{"mine":{"baseUrl":"https://example.test/v1","apiKey":"secret-user-key","models":[{"id":"x"}]}}}`
	got := importModels(t, raw, "work", "http://127.0.0.1:8317", []PiImportModel{{ID: "gemini-3.8-flash"}})
	if !strings.Contains(got, `"mine"`) || !strings.Contains(got, "secret-user-key") {
		t.Fatalf("user provider was changed: %s", got)
	}
}

func TestPiImportRefusesUserOwnedAccountProvider(t *testing.T) {
	// A user who already named a provider peaproxy-work must not have it
	// overwritten: that provider has no owned marker.
	raw := `{"providers":{"peaproxy-work":{"baseUrl":"https://user.test/v1","apiKey":"their-key","models":[{"id":"keep"}]}}}`
	if _, err := importPi(a(raw), "work", "http://127.0.0.1:8317", []PiImportModel{{ID: "gemini-3.8-flash", AccountID: "work"}}); err == nil {
		t.Fatalf("expected refusal for a user-owned peaproxy-work provider")
	}
}

func TestFetchPiModelsUsesAdminCatalogAndPreservesAccountMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/catalog" || r.URL.Query().Get("filter") != "all" {
			t.Fatalf("request path/query = %s", r.URL.String())
		}
		if r.Header.Get("X-Admin-Token") != "fixture-token" {
			t.Fatalf("admin token missing")
		}
		_, _ = io.WriteString(w, `{"models":[{"id":"gemini-3.8-flash","accountId":"work","modalities":["text","image_in"],"contextWindow":1048576,"routable":true},{"id":"gemini-hidden","accountId":"work","modalities":["text"],"routable":false},{"id":"claude-sonnet-5","accountId":"work","modalities":["text","image"],"routable":true}]}`)
	}))
	defer server.Close()
	models, err := FetchPiModels(context.Background(), server.URL, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "gemini-3.8-flash" || models[0].AccountID != "work" || models[0].ContextWindow != 1048576 || strings.Join(models[0].Input, ",") != "text,image" {
		t.Fatalf("models = %+v", models)
	}
	if models[1].ID != "claude-sonnet-5" {
		t.Fatalf("non-routable model was included or model lost: %+v", models)
	}
}

func TestFetchPiModelsDoesNotForwardAdminTokenAcrossRedirect(t *testing.T) {
	var received string
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("X-Admin-Token")
		_, _ = io.WriteString(w, `{"models":[]}`)
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/admin/catalog?filter=all", http.StatusFound)
	}))
	defer redirect.Close()
	_, err := FetchPiModels(context.Background(), redirect.URL, "secret-admin")
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("expected redirect to be rejected, got %v", err)
	}
	if received != "" {
		t.Fatalf("admin token forwarded to redirect target")
	}
}

func TestPiRemovalDropsOwnedAccountProviderOnly(t *testing.T) {
	raw := importModels(t, `{"providers":{"mine":{"baseUrl":"https://example.test/v1","apiKey":"secret-user-key"}}}`,
		"work", "http://127.0.0.1:8317", []PiImportModel{{ID: "gemini-3.8-flash", AccountID: "work"}})
	out, err := removePi(a(raw))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "peaproxy-work") {
		t.Fatalf("owned import survived disconnect: %s", out)
	}
	if !strings.Contains(string(out), "secret-user-key") {
		t.Fatalf("user provider lost on disconnect: %s", out)
	}
}
