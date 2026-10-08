package opencodezen

import (
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

func TestListModelsTagsZenFreeAndPrivacyModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("User-Agent") != UserAgent || r.Header.Get("X-Opencode-Client") != "cli" || r.Header.Get("X-Opencode-Session") != "fixed-session" {
			t.Errorf("headers = %#v", r.Header)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"Big Pickle"},{"id":"muse-1"},{"id":"kimi-k3"}]}`)
	}))
	t.Cleanup(srv.Close)

	adp, err := New(adapter.Options{ID: "zen-account", BaseURL: srv.URL + "/v1", SessionID: "fixed-session", ExtraHeaders: map[string]string{"x-opencode-request": "fixed-request"}})
	if err != nil {
		t.Fatal(err)
	}
	models, err := adp.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("models = %#v", models)
	}
	if models[0].Provider != Name || models[0].AccountID != "zen-account" || models[0].Tier != catalog.TierFree {
		t.Errorf("free model = %#v", models[0])
	}
	if models[1].PrivacyNote == "" || models[1].Tier != catalog.TierPaid || models[2].Tier != catalog.TierPaid {
		t.Errorf("model tags = %#v", models)
	}
}

func TestFreeChatForcesStreamingAndAddsRequiredTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("request = %s headers=%#v", r.URL.Path, r.Header)
		}
		var body struct {
			Model  string            `json:"model"`
			Stream bool              `json:"stream"`
			Tools  []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "space-bunny-free" || !body.Stream || len(body.Tools) != 2 {
			t.Errorf("free request = %#v", body)
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"zen ok\"}}]}\n\ndata: [DONE]\n")
	}))
	t.Cleanup(srv.Close)

	adp, err := New(adapter.Options{BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := adp.Chat(context.Background(), adapter.ChatRequest{Model: "space-bunny-free", Messages: []adapter.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "zen ok" || resp.Model != "space-bunny-free" {
		t.Errorf("response = %#v", resp)
	}
}

func TestFreeHelpersPreserveCallerToolsAndRecognizeNames(t *testing.T) {
	raw := ensureFreeTools([]byte(`{"tools":[{"type":"function"}]}`))
	if string(raw) != `{"tools":[{"type":"function"}]}` {
		t.Errorf("caller tools changed: %s", raw)
	}
	for _, id := range []string{"x-free", "x-free-y", "Big Pickle", "Space Bunny", "Jev 1.13"} {
		if !looksFree(id) {
			t.Errorf("looksFree(%q) = false", id)
		}
	}
	if looksFree("regular-model") || !strings.Contains(privacyNote("MIMO model"), "training") || privacyNote("regular-model") != "" {
		t.Error("free/privacy helper classification changed")
	}
}
