package opencodego

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

func TestOpenCodeGoListChatAndSessionHeader(t *testing.T) {
	var ua, session, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			ua = r.Header.Get("User-Agent")
			session = r.Header.Get("X-Opencode-Session")
			auth = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{
					{"id": "space-bunny-free"},
					{"id": "muse-spark-1.3-contributor"},
					{"id": "kimi-k3"},
				},
			})
		case "/v1/chat/completions":
			raw, _ := io.ReadAll(r.Body)
			if strings.Contains(string(raw), `"stream":true`) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"go-ok\"}}]}\n\n")
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "go-ok"}}},
			})
		default:
			t.Errorf("path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	a, err := New(adapter.Options{ID: "opencode-go", BaseURL: srv.URL + "/v1", APIKey: "sk-go"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 3 {
		t.Fatalf("%v %#v", err, models)
	}
	if models[0].Provider != Name || models[0].Tier != catalog.TierFree {
		t.Fatalf("free: %#v", models[0])
	}
	if models[1].PrivacyNote == "" || !strings.Contains(strings.ToLower(models[1].ID), "muse") {
		t.Fatalf("muse privacy: %#v", models[1])
	}
	if models[2].Tier != catalog.TierPaid {
		t.Fatalf("paid: %#v", models[2])
	}
	if ua != UserAgent || session == "" || auth != "Bearer sk-go" {
		t.Fatalf("headers ua=%q session=%q auth=%q", ua, session, auth)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "kimi-k3",
		Raw:   []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil || resp.Content != "go-ok" {
		t.Fatalf("%v %#v", err, resp)
	}
	var buf bytes.Buffer
	if err := a.ChatStream(context.Background(), adapter.ChatRequest{
		Model: "kimi-k3",
		Raw:   []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}]}`),
	}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "go-ok") {
		t.Fatalf("stream %s", buf.String())
	}
}

func TestOpenCodeGoAuthIsAPIKeyNotOAuth(t *testing.T) {
	adp, err := New(adapter.Options{ID: "opencode-go"})
	if err != nil {
		t.Fatal(err)
	}
	if adp.Capabilities().OAuth {
		t.Fatal("OpenCode Go is an API-key adapter, not subscription OAuth")
	}
	auth, ok := adp.(adapter.Authenticator)
	if !ok {
		t.Fatal("Authenticator so auth login can explain the API-key path")
	}
	_, err = auth.AuthStart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "opencode.ai/auth") {
		t.Fatalf("want console URL, got %v", err)
	}
	if DefaultBaseURL == "https://opencode.ai/zen/v1" || !strings.Contains(DefaultBaseURL, "/zen/go/v1") {
		t.Fatalf("must be distinct from Zen: %s", DefaultBaseURL)
	}
}
