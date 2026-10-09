package clients

import (
	"encoding/json"
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
		{ID: "gemini-3.8-flash", Reasoning: true, Input: []string{"text", "image"}, ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "claude-sonnet-5"}, // built-in Anthropic: must be skipped
		{ID: "gpt-5.5"},         // built-in OpenAI: must be skipped
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

func TestPiImportIsIdempotent(t *testing.T) {
	models := []PiImportModel{{ID: "gemini-3.8-flash"}}
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
	if _, err := importPi(a(raw), "work", "http://127.0.0.1:8317", []PiImportModel{{ID: "gemini-3.8-flash"}}); err == nil {
		t.Fatalf("expected refusal for a user-owned peaproxy-work provider")
	}
}

func TestPiRemovalDropsOwnedAccountProviderOnly(t *testing.T) {
	raw := importModels(t, `{"providers":{"mine":{"baseUrl":"https://example.test/v1","apiKey":"secret-user-key"}}}`,
		"work", "http://127.0.0.1:8317", []PiImportModel{{ID: "gemini-3.8-flash"}})
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
