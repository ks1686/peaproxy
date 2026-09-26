package translate

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesToOpenAIStringInput(t *testing.T) {
	in := []byte(`{"model":"llama3.2","instructions":"be brief","input":"ping"}`)
	body, req, err := ResponsesToOpenAI(in)
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "llama3.2" {
		t.Fatalf("model %s", req.Model)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Content != "ping" {
		t.Fatalf("%#v", req.Messages)
	}
	if !bytes.Contains(body, []byte(`"messages"`)) || bytes.Contains(body, []byte(`"input"`)) {
		t.Fatalf("%s", body)
	}
}

func TestResponsesToOpenAIArrayInput(t *testing.T) {
	in := []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	_, req, err := ResponsesToOpenAI(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Content != "hi" {
		t.Fatalf("%#v", req.Messages)
	}
}

func TestFromOpenAIChatWrapsOutputText(t *testing.T) {
	in := []byte(`{"id":"chatcmpl-1","model":"llama3.2","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`)
	out, err := FromOpenAIChat(in, "llama3.2")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Object     string `json:"object"`
		Status     string `json:"status"`
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Object != "response" || parsed.Status != "completed" || parsed.OutputText != "hello" {
		t.Fatalf("%s", out)
	}
	if len(parsed.Output) != 1 || parsed.Output[0].Content[0].Type != "output_text" {
		t.Fatalf("%s", out)
	}
}

func TestLooksLikeResponses(t *testing.T) {
	if !LooksLikeResponses([]byte(`{"model":"m","input":"hi"}`)) {
		t.Fatal("string input")
	}
	if LooksLikeResponses([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)) {
		t.Fatal("chat must not look like responses")
	}
}

func TestOpenAISSEToResponses(t *testing.T) {
	in := strings.NewReader("data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\ndata: [DONE]\n\n")
	var out bytes.Buffer
	if err := OpenAISSEToResponses(in, &out, "m"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, ev := range []string{"event: response.created", "event: response.output_text.delta", "event: response.completed"} {
		if !strings.Contains(got, ev) {
			t.Fatalf("missing %s in %s", ev, got)
		}
	}
	if !strings.Contains(got, `"delta":"hel"`) || !strings.Contains(got, `"delta":"lo"`) {
		t.Fatalf("%s", got)
	}
}
