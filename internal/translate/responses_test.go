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

func TestResponsesToOpenAIMapsFunctionCallItems(t *testing.T) {
	in := []byte(`{
		"model":"m",
		"tools":[{"type":"function","name":"lookup","description":"find","parameters":{"type":"object"}}],
		"tool_choice":"auto",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"look"}]},
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"found"},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"skip me"}]}
		]
	}`)
	body, req, err := ResponsesToOpenAI(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"tools"`)) || !bytes.Contains(body, []byte(`"tool_choice"`)) {
		t.Fatalf("missing tools: %s", body)
	}
	if !bytes.Contains(body, []byte(`"tool_calls"`)) || !bytes.Contains(body, []byte(`"call_1"`)) {
		t.Fatalf("function_call must become assistant tool_calls: %s", body)
	}
	if !bytes.Contains(body, []byte(`"role":"tool"`)) || !bytes.Contains(body, []byte(`"tool_call_id":"call_1"`)) {
		t.Fatalf("function_call_output must become tool message: %s", body)
	}
	if bytes.Contains(messageContents(t, body), []byte("skip me")) {
		t.Fatalf("reasoning must not be invented as chat text: %s", body)
	}
	if !bytes.Contains(body, []byte(`"reasoning_opaque"`)) || !bytes.Contains(body, []byte("skip me")) {
		t.Fatalf("reasoning item must be carried opaquely: %s", body)
	}
	if req.Model != "m" {
		t.Fatalf("model %s", req.Model)
	}
	var parsed struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tools) != 1 || parsed.Tools[0].Function.Name != "lookup" {
		t.Fatalf("chat-shaped tools: %s", body)
	}
}

func TestFromOpenAIChatMapsToolCalls(t *testing.T) {
	in := []byte(`{
		"id":"chatcmpl-1",
		"model":"m",
		"choices":[{
			"message":{
				"role":"assistant",
				"content":null,
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]
			},
			"finish_reason":"tool_calls"
		}]
	}`)
	out, err := FromOpenAIChat(in, "m")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"type":"function_call"`)) || !bytes.Contains(out, []byte(`"call_id":"call_1"`)) {
		t.Fatalf("missing function_call output item: %s", out)
	}
	if !bytes.Contains(out, []byte(`"name":"lookup"`)) {
		t.Fatalf("%s", out)
	}
	if bytes.Contains(out, []byte(`"output":"found"`)) {
		t.Fatal("must not invent tool execution results")
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

func messageContents(t *testing.T, raw []byte) []byte {
	t.Helper()
	var parsed struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	for _, m := range parsed.Messages {
		b.Write(m.Content)
	}
	return b.Bytes()
}

func TestOpenAISSEToResponsesCarriesOpaqueReasoning(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		`data: {"id":"c1","model":"m","choices":[{"delta":{"content":"hello"}}]}`,
		``,
		`data: {"choices":[{"delta":{"reasoning_opaque":[{"kind":"responses_reasoning","id":"rs_9","encrypted_content":"enc","summary":[{"type":"summary_text","text":"skip me"}]}]}}]}`,
		``,
		`data: {"choices":[{"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n"))
	var out bytes.Buffer
	if err := OpenAISSEToResponses(in, &out, "m"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, `"output_text":"skip me"`) || strings.Contains(got, `"delta":"skip me"`) {
		t.Fatalf("reasoning invented as text: %s", got)
	}
	if !strings.Contains(got, `"type":"reasoning"`) || !strings.Contains(got, `"id":"rs_9"`) || !strings.Contains(got, `"encrypted_content":"enc"`) {
		t.Fatalf("missing reasoning item: %s", got)
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

func TestOpenAISSEToResponsesMapsToolCalls(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		`data: {"id":"c1","model":"m","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":""}}]}}]}`,
		``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":\"x\"}"}}]}}]}`,
		``,
		`data: {"choices":[{"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n"))
	var out bytes.Buffer
	if err := OpenAISSEToResponses(in, &out, "m"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `"type":"function_call"`) || !strings.Contains(got, `"call_id":"call_1"`) {
		t.Fatalf("completed output missing function_call: %s", got)
	}
	if !strings.Contains(got, `"name":"lookup"`) || !strings.Contains(got, `\"q\":\"x\"`) {
		t.Fatalf("%s", got)
	}
	if strings.Contains(got, `"output":"`) && strings.Contains(got, "executed") {
		t.Fatal("must not invent tool execution")
	}
}
