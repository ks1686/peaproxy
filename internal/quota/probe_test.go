package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeURLOnlyOpenRouter(t *testing.T) {
	if _, ok := ProbeURL("openai", "https://api.openai.com/v1"); ok {
		t.Fatal("openai must not have a remaining GET probe")
	}
	if _, ok := ProbeURL("copilot_oauth", ""); ok {
		t.Fatal("copilot internal quota APIs are skipped")
	}
	u, ok := ProbeURL("openrouter", "")
	if !ok || u != "https://openrouter.ai/api/v1/key" {
		t.Fatalf("%v %s", ok, u)
	}
	u, ok = ProbeURL("openrouter", "http://127.0.0.1:9/v1/")
	if !ok || u != "http://127.0.0.1:9/v1/key" {
		t.Fatalf("%v %s", ok, u)
	}
}

func TestProbeOpenRouterCredits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/key" {
			t.Errorf("path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-or-test" {
			t.Errorf("auth %s", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"limit":           100,
				"limit_remaining": 74.5,
				"usage":           25.5,
				"is_free_tier":    false,
			},
		})
	}))
	t.Cleanup(srv.Close)
	snap, err := ProbeOpenRouter(context.Background(), srv.Client(), "or1", "sk-or-test", srv.URL+"/api/v1/key")
	if err != nil {
		t.Fatal(err)
	}
	if snap.RemainingCredits == nil || *snap.RemainingCredits != 74.5 {
		t.Fatalf("%#v", snap)
	}
	if snap.CreditsUnlimited {
		t.Fatal("capped key must not be marked unlimited")
	}
}

func TestProbeOpenRouterNullRemainingIsDocumentedUnlimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"limit":null,"limit_remaining":null,"usage":1.25}}`))
	}))
	t.Cleanup(srv.Close)
	snap, err := ProbeOpenRouter(context.Background(), srv.Client(), "or1", "sk-or-test", srv.URL+"/key")
	if err != nil {
		t.Fatal(err)
	}
	if !snap.CreditsUnlimited {
		t.Fatalf("OpenRouter null remaining is documented unlimited: %#v", snap)
	}
	if snap.RemainingCredits != nil {
		t.Fatalf("unlimited must not invent a remaining number: %#v", snap)
	}
}

func TestProbeOpenRouterHTTPErrorDoesNotInventZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	}))
	t.Cleanup(srv.Close)
	snap, err := ProbeOpenRouter(context.Background(), srv.Client(), "or1", "sk-or-test", srv.URL+"/key")
	if err == nil {
		t.Fatal("want error")
	}
	if snap.Reported() {
		t.Fatalf("failed probe must not invent remaining: %#v", snap)
	}
}

func TestQuotaProbeURLPolicy(t *testing.T) {
	allow := []string{
		"https://openrouter.ai/api/v1/key",
		"https://example.com/v1/key",
		"http://127.0.0.1:9/v1/key",
		"http://localhost:11434/key",
		"http://[::1]:9/key",
		"https://10.0.0.5/key",
	}
	for _, raw := range allow {
		if !openRouterProbeURL.MatchString(raw) || !quotaProbeURLAllowed(raw) {
			t.Errorf("allowed %q", raw)
		}
	}
	deny := []string{
		"http://169.254.169.254/latest/meta-data",
		"https://169.254.169.254/latest",
		"http://example.com/key",
		"file:///etc/passwd",
		"https://user:pass@openrouter.ai/api/v1/key",
		"https://metadata.google.internal/computeMetadata/v1/",
		"http://0.0.0.0:9/key",
		"https://openrouter.ai/key\n",
	}
	for _, raw := range deny {
		if openRouterProbeURL.MatchString(raw) && quotaProbeURLAllowed(raw) {
			t.Errorf("must reject %q", raw)
		}
	}
	_, err := ProbeOpenRouter(context.Background(), nil, "or1", "sk-or-test", "http://169.254.169.254/latest")
	if err == nil {
		t.Fatal("metadata URL must not be requested")
	}
}

func TestProbeOpenRouterRequiresKey(t *testing.T) {
	_, err := ProbeOpenRouter(context.Background(), nil, "or1", "", "https://openrouter.ai/api/v1/key")
	if err == nil {
		t.Fatal("want key required")
	}
}

func TestCanProbe(t *testing.T) {
	if !CanProbe("openrouter") {
		t.Fatal("openrouter")
	}
	if CanProbe("anthropic") || CanProbe("copilot_oauth") || CanProbe("ollama") {
		t.Fatal("only OpenRouter has a documented remaining GET")
	}
}
