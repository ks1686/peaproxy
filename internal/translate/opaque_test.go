package translate

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestToOpenAICarriesAssistantThinking(t *testing.T) {
	in := []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"secret chain","signature":"sig_1"},{"type":"text","text":"hello"},{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"x"}}]}]}`)
	out, _, err := ToOpenAI(in)
	if err != nil {
		t.Fatal(err)
	}
	msg := assistantOpaque(t, out)
	if msg.Content != "hello" {
		t.Fatalf("content %q", msg.Content)
	}
	if strings.Contains(msg.Content, "secret chain") {
		t.Fatalf("thinking leaked into content: %s", out)
	}
	if !bytes.Contains(out, []byte(`"toolu_1"`)) {
		t.Fatalf("tool call dropped: %s", out)
	}
	ent := mustOpaqueKind(t, msg.Opaque, "anthropic_thinking")
	if ent.Thinking != "secret chain" || ent.Signature != "sig_1" {
		t.Fatalf("opaque %#v", ent)
	}
	if bytes.Contains(out, []byte(`"responses_reasoning"`)) {
		t.Fatalf("invented responses kind: %s", out)
	}
}

func TestToOpenAICarriesSignatureOnlyThinking(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig_only"}]}]}`)
	out, _, err := ToOpenAI(in)
	if err != nil {
		t.Fatal(err)
	}
	msg := assistantOpaque(t, out)
	if msg.Content != "" {
		t.Fatalf("content %q, want empty", msg.Content)
	}
	ent := mustOpaqueKind(t, msg.Opaque, "anthropic_thinking")
	if ent.Signature != "sig_only" {
		t.Fatalf("opaque %#v", ent)
	}
}

func TestToOpenAICarriesRedactedThinking(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"abc"},{"type":"text","text":"hi"}]}]}`)
	out, _, err := ToOpenAI(in)
	if err != nil {
		t.Fatal(err)
	}
	msg := assistantOpaque(t, out)
	if msg.Content != "hi" {
		t.Fatalf("content %q", msg.Content)
	}
	ent := mustOpaqueKind(t, msg.Opaque, "anthropic_redacted_thinking")
	if ent.Data != "abc" {
		t.Fatalf("opaque %#v", ent)
	}
}

func TestToClaudeRestoresAnthropicThinkingFirst(t *testing.T) {
	in := []byte(`{
		"model":"claude-sonnet-4-5",
		"messages":[{
			"role":"assistant",
			"content":"hello",
			"tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}],
			"reasoning_opaque":[
				{"kind":"responses_reasoning","id":"rs_1","encrypted_content":"enc","summary":[{"type":"summary_text","text":"nope"}]},
				{"kind":"anthropic_thinking","thinking":"secret chain","signature":"sig_1"},
				{"kind":"anthropic_redacted_thinking","data":"red"}
			]
		}]
	}`)
	out, err := ToClaude(in, false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(`"encrypted_content"`)) || bytes.Contains(out, []byte(`"rs_1"`)) || bytes.Contains(out, []byte("nope")) {
		t.Fatalf("responses kind leaked into Claude body: %s", out)
	}
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				Text      string `json:"text"`
				Thinking  string `json:"thinking"`
				Signature string `json:"signature"`
				Data      string `json:"data"`
				ID        string `json:"id"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if len(parsed.Messages) != 1 {
		t.Fatalf("messages %#v", parsed.Messages)
	}
	blocks := parsed.Messages[0].Content
	if len(blocks) != 4 {
		t.Fatalf("blocks %#v from %s", blocks, out)
	}
	if blocks[0].Type != "thinking" || blocks[0].Thinking != "secret chain" || blocks[0].Signature != "sig_1" {
		t.Fatalf("first %#v", blocks[0])
	}
	if blocks[1].Type != "redacted_thinking" || blocks[1].Data != "red" {
		t.Fatalf("second %#v", blocks[1])
	}
	if blocks[2].Type != "text" || blocks[2].Text != "hello" {
		t.Fatalf("third %#v", blocks[2])
	}
	if blocks[3].Type != "tool_use" || blocks[3].ID != "toolu_1" {
		t.Fatalf("fourth %#v", blocks[3])
	}
}

func TestFromClaudeCarriesThinkingOpaque(t *testing.T) {
	in := []byte(`{"id":"msg_1","model":"c","content":[{"type":"thinking","thinking":"secret chain","signature":"sig_1"},{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`)
	out, err := FromClaude(in)
	if err != nil {
		t.Fatal(err)
	}
	msg := assistantOpaque(t, out)
	if msg.Content != "hello" {
		t.Fatalf("content %q in %s", msg.Content, out)
	}
	ent := mustOpaqueKind(t, msg.Opaque, "anthropic_thinking")
	if ent.Thinking != "secret chain" || ent.Signature != "sig_1" {
		t.Fatalf("opaque %#v", ent)
	}
}

func TestFromOpenAIRestoresThinkingBeforeText(t *testing.T) {
	in := []byte(`{
		"id":"chatcmpl-1",
		"model":"c",
		"choices":[{
			"message":{
				"role":"assistant",
				"content":"hello",
				"reasoning_opaque":[
					{"kind":"responses_reasoning","id":"rs_1","encrypted_content":"enc"},
					{"kind":"anthropic_thinking","thinking":"secret chain","signature":"sig_1"}
				]
			},
			"finish_reason":"stop"
		}]
	}`)
	out, err := FromOpenAI(in, "c")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(`"rs_1"`)) || bytes.Contains(out, []byte(`"encrypted_content"`)) {
		t.Fatalf("responses kind became a Claude block: %s", out)
	}
	var parsed struct {
		Content []struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Content) < 2 || parsed.Content[0].Type != "thinking" || parsed.Content[0].Signature != "sig_1" {
		t.Fatalf("content %#v from %s", parsed.Content, out)
	}
	if parsed.Content[1].Type != "text" || parsed.Content[1].Text != "hello" {
		t.Fatalf("text %#v", parsed.Content[1])
	}
}

func TestResponsesToOpenAICarriesReasoningNotAsText(t *testing.T) {
	in := []byte(`{
		"model":"m",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"look"}]},
			{"type":"reasoning","id":"rs_9","encrypted_content":"enc","status":"completed","summary":[{"type":"summary_text","text":"skip me"}]},
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}
		]
	}`)
	body, _, err := ResponsesToOpenAI(in)
	if err != nil {
		t.Fatal(err)
	}
	msg := assistantOpaque(t, body)
	if strings.Contains(msg.Content, "skip me") {
		t.Fatalf("reasoning invented as chat text: %s", body)
	}
	ent := mustOpaqueKind(t, msg.Opaque, "responses_reasoning")
	if ent.ID != "rs_9" || ent.EncryptedContent != "enc" || ent.Status != "completed" {
		t.Fatalf("opaque %#v", ent)
	}
	if !bytes.Contains(ent.Summary, []byte(`"summary_text"`)) || !bytes.Contains(ent.Summary, []byte("skip me")) {
		t.Fatalf("summary not preserved: %s", ent.Summary)
	}
	if !bytes.Contains(body, []byte(`"call_1"`)) {
		t.Fatalf("tool call dropped: %s", body)
	}
}

func TestFromOpenAIChatRestoresResponsesReasoning(t *testing.T) {
	in := []byte(`{
		"id":"chatcmpl-1",
		"model":"m",
		"choices":[{
			"message":{
				"role":"assistant",
				"content":"hello",
				"reasoning_opaque":[
					{"kind":"anthropic_thinking","thinking":"nope","signature":"sig"},
					{"kind":"responses_reasoning","id":"rs_9","encrypted_content":"enc","status":"completed","summary":[{"type":"summary_text","text":"skip me"}]}
				]
			},
			"finish_reason":"stop"
		}]
	}`)
	out, err := FromOpenAIChat(in, "m")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(`"signature"`)) || bytes.Contains(out, []byte("nope")) {
		t.Fatalf("anthropic kind leaked into Responses: %s", out)
	}
	if bytes.Contains(out, []byte(`"output_text":"skip me"`)) {
		t.Fatalf("reasoning invented as output text: %s", out)
	}
	var parsed struct {
		Output []struct {
			Type             string          `json:"type"`
			ID               string          `json:"id"`
			EncryptedContent string          `json:"encrypted_content"`
			Status           string          `json:"status"`
			Summary          json.RawMessage `json:"summary"`
			Role             string          `json:"role"`
			Content          []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Output) < 2 || parsed.Output[0].Type != "reasoning" || parsed.Output[0].ID != "rs_9" || parsed.Output[0].EncryptedContent != "enc" {
		t.Fatalf("output %#v from %s", parsed.Output, out)
	}
	if !bytes.Contains(parsed.Output[0].Summary, []byte("skip me")) {
		t.Fatalf("summary %s", parsed.Output[0].Summary)
	}
	if parsed.Output[1].Type != "message" || parsed.Output[1].Content[0].Text != "hello" {
		t.Fatalf("message %#v", parsed.Output[1])
	}
}

func TestStripReasoningOpaquePreservesOtherKeys(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"assistant","content":"hello","reasoning_opaque":[{"kind":"anthropic_thinking","signature":"sig"}],"tool_calls":[{"id":"t"}]}],"stream":false}`)
	out := StripReasoningOpaque(in)
	if bytes.Contains(out, []byte("reasoning_opaque")) || bytes.Contains(out, []byte("sig")) {
		t.Fatalf("opaque remained: %s", out)
	}
	if !bytes.Contains(out, []byte(`"model":"m"`)) || !bytes.Contains(out, []byte(`"content":"hello"`)) || !bytes.Contains(out, []byte(`"tool_calls"`)) {
		t.Fatalf("stripped too much: %s", out)
	}
	modelIdx := bytes.Index(out, []byte(`"model"`))
	messagesIdx := bytes.Index(out, []byte(`"messages"`))
	streamIdx := bytes.Index(out, []byte(`"stream"`))
	if modelIdx < 0 || messagesIdx < modelIdx || streamIdx < messagesIdx {
		t.Fatalf("key order changed: %s", out)
	}
}

func TestClaudeSSEToOpenAIEmitsOpaqueBeforeFinish(t *testing.T) {
	in := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_t","model":"c"}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"secret chain"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_1"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hello"}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")
	var buf bytes.Buffer
	if err := ClaudeSSEToOpenAI(strings.NewReader(in), &buf); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, `"content":"secret chain"`) || strings.Contains(got, `"content":"secret`) {
		t.Fatalf("thinking streamed as content: %s", got)
	}
	payloads := sseDataPayloads(got)
	opaqueAt, finishAt, contentAt := -1, -1, -1
	for i, p := range payloads {
		if strings.Contains(p, `"reasoning_opaque"`) {
			opaqueAt = i
		}
		if strings.Contains(p, `"finish_reason"`) {
			finishAt = i
		}
		if strings.Contains(p, `"content":"hello"`) {
			contentAt = i
		}
	}
	if opaqueAt < 0 || finishAt < 0 || opaqueAt >= finishAt {
		t.Fatalf("opaque at %d finish at %d\n%s", opaqueAt, finishAt, got)
	}
	if contentAt < 0 || strings.Count(got, `"reasoning_opaque"`) != 1 {
		t.Fatalf("content %d count opaque in %s", contentAt, got)
	}
	if !strings.Contains(got, `"signature":"sig_1"`) || !strings.Contains(got, "secret chain") {
		t.Fatalf("missing signature carry: %s", got)
	}
}

type assistantCarry struct {
	Content string
	Opaque  json.RawMessage
}

func assistantOpaque(t *testing.T, raw []byte) assistantCarry {
	t.Helper()
	var parsed struct {
		Messages []struct {
			Role            string          `json:"role"`
			Content         json.RawMessage `json:"content"`
			ReasoningOpaque json.RawMessage `json:"reasoning_opaque"`
		} `json:"messages"`
		Choices []struct {
			Message struct {
				Role            string          `json:"role"`
				Content         json.RawMessage `json:"content"`
				ReasoningOpaque json.RawMessage `json:"reasoning_opaque"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse: %v %s", err, raw)
	}
	var role string
	var content, opaque json.RawMessage
	switch {
	case len(parsed.Choices) > 0:
		role = parsed.Choices[0].Message.Role
		content = parsed.Choices[0].Message.Content
		opaque = parsed.Choices[0].Message.ReasoningOpaque
	default:
		for _, m := range parsed.Messages {
			if m.Role == "assistant" && (len(m.ReasoningOpaque) > 0 || len(m.Content) > 0) {
				role = m.Role
				content = m.Content
				opaque = m.ReasoningOpaque
				if len(m.ReasoningOpaque) > 0 {
					break
				}
			}
		}
	}
	if role != "assistant" && len(parsed.Messages) > 0 {
		for _, m := range parsed.Messages {
			if m.Role == "assistant" {
				role = m.Role
				content = m.Content
				opaque = m.ReasoningOpaque
				break
			}
		}
	}
	if role != "assistant" {
		t.Fatalf("no assistant message in %s", raw)
	}
	text := ""
	if len(content) > 0 && string(content) != "null" {
		if err := json.Unmarshal(content, &text); err != nil {
			t.Fatalf("content %s: %v", content, err)
		}
	}
	return assistantCarry{Content: text, Opaque: opaque}
}

type opaqueProbe struct {
	Kind             string          `json:"kind"`
	Thinking         string          `json:"thinking"`
	Signature        string          `json:"signature"`
	Data             string          `json:"data"`
	ID               string          `json:"id"`
	EncryptedContent string          `json:"encrypted_content"`
	Summary          json.RawMessage `json:"summary"`
	Status           string          `json:"status"`
}

func mustOpaqueKind(t *testing.T, raw json.RawMessage, kind string) opaqueProbe {
	t.Helper()
	var entries []opaqueProbe
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("opaque %s: %v", raw, err)
	}
	for _, e := range entries {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("missing kind %s in %s", kind, raw)
	return opaqueProbe{}
}
