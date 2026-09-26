package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToOpenAIUsesStructsNotMaps(t *testing.T) {
	in := []byte(`{"model":"llama3.2","system":"be brief","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`)
	out, req, err := ToOpenAI(in)
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "llama3.2" {
		t.Fatalf("model %s", req.Model)
	}
	if !strings.Contains(string(out), `"role":"system"`) || !strings.Contains(string(out), `"role":"user"`) {
		t.Fatalf("%s", out)
	}
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Messages) != 2 {
		t.Fatalf("%#v", parsed.Messages)
	}
}

func TestRefusesEmptyAfterDroppingNonText(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"tool_use","id":"x"}]}]}`)
	_, _, err := ToOpenAI(in)
	if err == nil {
		t.Fatal("expected error for silent tool_use drop")
	}
}

func TestToOpenAIPreservesVision(t *testing.T) {
	in := []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"what"},{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}]}`)
	out, _, err := ToOpenAI(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"image_url"`) || !strings.Contains(string(out), "https://example.com/a.png") {
		t.Fatalf("%s", out)
	}
}

func TestFromOpenAI(t *testing.T) {
	in := []byte(`{"id":"chatcmpl-1","model":"llama3.2","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`)
	out, err := FromOpenAI(in, "llama3.2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"type":"message"`) || !strings.Contains(string(out), `"hello"`) {
		t.Fatalf("%s", out)
	}
}
