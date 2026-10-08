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

func TestVerifyModelsAndChat(t *testing.T) {
	var chatPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data":   []map[string]string{{"id": "llama3.2"}},
			})
		case "/v1/chat/completions", "/v1/messages", "/v1/responses":
			chatPath = r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(raw), `"ping"`) {
				t.Errorf("body %s", raw)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	res, err := Verify(context.Background(), "cursor", srv.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.ModelsN != 1 || !res.ChatOK || chatPath != "/v1/chat/completions" {
		t.Fatalf("%#v path=%s", res, chatPath)
	}

	res2, err := Verify(context.Background(), "claude-code", srv.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	if chatPath != "/v1/messages" || !res2.ChatOK {
		t.Fatalf("claude-code wire path=%s %#v", chatPath, res2)
	}

	res3, err := Verify(context.Background(), "codex", srv.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	if chatPath != "/v1/responses" || !res3.ChatOK {
		t.Fatalf("codex wire path=%s %#v", chatPath, res3)
	}

	res4, err := Verify(context.Background(), "pi", srv.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res4.Wires) != 2 || res4.Wires[0].Wire != WireChat || res4.Wires[1].Wire != WireMessages {
		t.Fatalf("pi wires %#v", res4.Wires)
	}

	res5, err := Verify(context.Background(), "amp", srv.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	if chatPath != "/v1/chat/completions" || !res5.ChatOK {
		t.Fatalf("amp wire path=%s %#v", chatPath, res5)
	}
}

func TestWiresForCoverage(t *testing.T) {
	if got := wiresFor("pi"); len(got) != 2 {
		t.Fatalf("pi verify must cover both wires: %v", got)
	}
	if got := wiresFor("amp"); len(got) != 1 || got[0] != WireChat {
		t.Fatalf("amp: %v", got)
	}
	if got := wiresFor("cline"); len(got) != 1 || got[0] != WireChat {
		t.Fatalf("cline: %v", got)
	}
	if got := wiresFor("continue"); got[0] != WireChat {
		t.Fatalf("continue: %v", got)
	}
	if got := wiresFor("codex"); got[0] != WireResponses {
		t.Fatalf("codex: %v", got)
	}
}

func TestVerifyUnknownClient(t *testing.T) {
	_, err := Verify(context.Background(), "nope", "http://127.0.0.1:9", false)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFormatVerify(t *testing.T) {
	s := FormatVerify(VerifyResult{Name: "cursor", ModelsURL: "http://x/v1/models", ModelsHTTP: 200, ModelsN: 2})
	if !strings.Contains(s, "models=2") {
		t.Fatalf("%s", s)
	}
}
