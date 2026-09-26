package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/jsonx"
)

// ResponsesRequest is a subset of POST /v1/responses (Codex / OpenAI Responses API).
type ResponsesRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions"`
	Input           json.RawMessage `json:"input"`
	Stream          bool            `json:"stream"`
	MaxOutputTokens int             `json:"max_output_tokens"`
	MaxTokens       int             `json:"max_tokens"`
	Tools           json.RawMessage `json:"tools"`
}

type responsesOutput struct {
	ID         string            `json:"id"`
	Object     string            `json:"object"`
	Status     string            `json:"status"`
	Model      string            `json:"model"`
	Output     []responsesOutMsg `json:"output"`
	OutputText string            `json:"output_text"`
}

type responsesOutMsg struct {
	Type    string             `json:"type"`
	Role    string             `json:"role"`
	Content []responsesOutPart `json:"content"`
}

type responsesOutPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// LooksLikeResponses reports whether raw is a Responses API request (has input, no chat messages).
func LooksLikeResponses(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	if !bytes.Contains(raw, []byte(`"input"`)) {
		return false
	}
	if bytes.Contains(raw, []byte(`"messages"`)) {
		return false
	}
	var p struct {
		Input json.RawMessage `json:"input"`
		Model string          `json:"model"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return false
	}
	return p.Model != "" && len(p.Input) > 0 && string(p.Input) != "null"
}

// ResponsesToOpenAI converts a Responses body into OpenAI chat.completions JSON (structs).
func ResponsesToOpenAI(raw []byte) ([]byte, adapter.ChatRequest, error) {
	var in ResponsesRequest
	if err := jsonx.DecodeStrict(raw, &in); err != nil {
		return nil, adapter.ChatRequest{}, err
	}
	if in.Model == "" {
		return nil, adapter.ChatRequest{}, fmt.Errorf("missing model")
	}
	msgs, err := responsesInputToMessages(in.Instructions, in.Input)
	if err != nil {
		return nil, adapter.ChatRequest{}, err
	}
	if len(msgs) == 0 {
		return nil, adapter.ChatRequest{}, fmt.Errorf("responses: empty input")
	}
	maxTok := in.MaxOutputTokens
	if maxTok <= 0 {
		maxTok = in.MaxTokens
	}
	out := openAIRequest{
		Model:     in.Model,
		Messages:  msgs,
		Stream:    in.Stream,
		MaxTokens: maxTok,
	}
	if len(in.Tools) > 0 && string(in.Tools) != "null" {
		out.Tools = in.Tools
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, adapter.ChatRequest{}, err
	}
	chatMsgs := make([]adapter.Message, 0, len(msgs))
	for _, m := range msgs {
		text, _ := responsesContentText(m.Content)
		chatMsgs = append(chatMsgs, adapter.Message{Role: m.Role, Content: text})
	}
	return body, adapter.ChatRequest{Model: in.Model, Messages: chatMsgs, Stream: in.Stream, Raw: body}, nil
}

func responsesInputToMessages(instructions string, input json.RawMessage) ([]openAIMessage, error) {
	var msgs []openAIMessage
	if instructions != "" {
		rawSys, err := json.Marshal(instructions)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, openAIMessage{Role: "system", Content: rawSys})
	}
	input = bytes.TrimSpace(input)
	if len(input) == 0 || string(input) == "null" {
		return msgs, nil
	}
	if input[0] == '"' {
		var s string
		if err := json.Unmarshal(input, &s); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		return append(msgs, openAIMessage{Role: "user", Content: raw}), nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, fmt.Errorf("responses input: %w", err)
	}
	for _, item := range items {
		msg, ok, err := responsesItemToMessage(item)
		if err != nil {
			return nil, err
		}
		if ok {
			msgs = append(msgs, msg)
		}
	}
	return msgs, nil
}

func responsesItemToMessage(item json.RawMessage) (openAIMessage, bool, error) {
	var parsed struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Text    string          `json:"text"`
	}
	if err := json.Unmarshal(item, &parsed); err != nil {
		return openAIMessage{}, false, err
	}
	role := strings.ToLower(parsed.Role)
	switch parsed.Type {
	case "", "message", "input_text", "output_text", "text":
		// keep
	default:
		return openAIMessage{}, false, nil
	}
	if role == "" {
		switch parsed.Type {
		case "output_text":
			role = "assistant"
		default:
			role = "user"
		}
	}
	if role == "developer" {
		role = "system"
	}
	text := parsed.Text
	if text == "" {
		t, err := responsesContentText(parsed.Content)
		if err != nil {
			return openAIMessage{}, false, err
		}
		text = t
	}
	raw, err := json.Marshal(text)
	if err != nil {
		return openAIMessage{}, false, err
	}
	return openAIMessage{Role: role, Content: raw}, true, nil
}

// FromOpenAIChat maps a chat.completion JSON object to a Responses object.
func FromOpenAIChat(raw []byte, model string) ([]byte, error) {
	var in openAIResponse
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	text := ""
	if len(in.Choices) > 0 {
		text = in.Choices[0].Message.Content
	}
	id := in.ID
	if id == "" {
		id = fmt.Sprintf("resp_%d", time.Now().UnixNano())
	}
	if in.Model != "" {
		model = in.Model
	}
	out := responsesOutput{
		ID:     id,
		Object: "response",
		Status: "completed",
		Model:  model,
		Output: []responsesOutMsg{{
			Type: "message",
			Role: "assistant",
			Content: []responsesOutPart{{
				Type: "output_text",
				Text: text,
			}},
		}},
		OutputText: text,
	}
	return json.Marshal(out)
}

// FromChatContent builds a Responses object from extracted assistant text.
func FromChatContent(id, model, text string) ([]byte, error) {
	if id == "" {
		id = fmt.Sprintf("resp_%d", time.Now().UnixNano())
	}
	out := responsesOutput{
		ID:     id,
		Object: "response",
		Status: "completed",
		Model:  model,
		Output: []responsesOutMsg{{
			Type: "message",
			Role: "assistant",
			Content: []responsesOutPart{{
				Type: "output_text",
				Text: text,
			}},
		}},
		OutputText: text,
	}
	return json.Marshal(out)
}

func responsesContentText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("responses content: %w", err)
	}
	var b strings.Builder
	for _, bl := range blocks {
		b.WriteString(bl.Text)
	}
	return b.String(), nil
}
