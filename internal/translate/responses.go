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
	ToolChoice      json.RawMessage `json:"tool_choice"`
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
	Type      string             `json:"type"`
	Role      string             `json:"role,omitempty"`
	Content   []responsesOutPart `json:"content,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
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
		converted, err := responsesToolsToChat(in.Tools)
		if err != nil {
			return nil, adapter.ChatRequest{}, err
		}
		out.Tools = converted
	}
	if len(in.ToolChoice) > 0 && string(in.ToolChoice) != "null" {
		out.ToolChoice = responsesToolChoiceToChat(in.ToolChoice)
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
	var pendingCalls []json.RawMessage
	flushCalls := func() error {
		if len(pendingCalls) == 0 {
			return nil
		}
		raw, err := json.Marshal(pendingCalls)
		if err != nil {
			return err
		}
		msgs = append(msgs, openAIMessage{Role: "assistant", Content: json.RawMessage("null"), ToolCalls: raw})
		pendingCalls = nil
		return nil
	}
	for _, item := range items {
		kind := responsesItemType(item)
		switch kind {
		case "function_call":
			call, err := responsesFunctionCallToToolCall(item)
			if err != nil {
				return nil, err
			}
			pendingCalls = append(pendingCalls, call)
		case "function_call_output":
			if err := flushCalls(); err != nil {
				return nil, err
			}
			msg, err := responsesFunctionOutputToMessage(item)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, msg)
		case "reasoning":
			// Chat completions have no reasoning item; skip rather than invent text.
			continue
		default:
			if err := flushCalls(); err != nil {
				return nil, err
			}
			msg, ok, err := responsesItemToMessage(item)
			if err != nil {
				return nil, err
			}
			if ok {
				msgs = append(msgs, msg)
			}
		}
	}
	if err := flushCalls(); err != nil {
		return nil, err
	}
	return msgs, nil
}

func responsesItemType(item json.RawMessage) string {
	var parsed struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(item, &parsed)
	return parsed.Type
}

func responsesFunctionCallToToolCall(item json.RawMessage) (json.RawMessage, error) {
	var parsed struct {
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(item, &parsed); err != nil {
		return nil, err
	}
	tc := struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}{ID: parsed.CallID, Type: "function"}
	tc.Function.Name = parsed.Name
	tc.Function.Arguments = parsed.Arguments
	return json.Marshal(tc)
}

func responsesFunctionOutputToMessage(item json.RawMessage) (openAIMessage, error) {
	var parsed struct {
		CallID string `json:"call_id"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(item, &parsed); err != nil {
		return openAIMessage{}, err
	}
	raw, err := json.Marshal(parsed.Output)
	if err != nil {
		return openAIMessage{}, err
	}
	return openAIMessage{Role: "tool", Content: raw, ToolCallID: parsed.CallID}, nil
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

func responsesToolsToChat(raw json.RawMessage) (json.RawMessage, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return raw, nil
	}
	out := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		converted, err := oneResponsesToolToChat(item)
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return json.Marshal(out)
}

func oneResponsesToolToChat(item json.RawMessage) (json.RawMessage, error) {
	var parsed struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Function    json.RawMessage `json:"function"`
	}
	if err := json.Unmarshal(item, &parsed); err != nil {
		return item, nil
	}
	if len(parsed.Function) > 0 && string(parsed.Function) != "null" {
		return item, nil
	}
	if parsed.Type == "function" && parsed.Name != "" {
		tool := struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description,omitempty"`
				Parameters  json.RawMessage `json:"parameters,omitempty"`
			} `json:"function"`
		}{Type: "function"}
		tool.Function.Name = parsed.Name
		tool.Function.Description = parsed.Description
		tool.Function.Parameters = parsed.Parameters
		return json.Marshal(tool)
	}
	return item, nil
}

func responsesToolChoiceToChat(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" || raw[0] == '"' {
		return raw
	}
	var parsed struct {
		Type     string          `json:"type"`
		Name     string          `json:"name"`
		Function json.RawMessage `json:"function"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return raw
	}
	if len(parsed.Function) > 0 && string(parsed.Function) != "null" {
		return raw
	}
	if parsed.Type == "function" && parsed.Name != "" {
		b, err := json.Marshal(struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}{Type: "function", Function: struct {
			Name string `json:"name"`
		}{Name: parsed.Name}})
		if err != nil {
			return raw
		}
		return b
	}
	return raw
}

// FromOpenAIChat maps a chat.completion JSON object to a Responses object.
func FromOpenAIChat(raw []byte, model string) ([]byte, error) {
	var in openAIResponse
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	text := ""
	var calls []struct {
		ID        string
		Name      string
		Arguments string
	}
	if len(in.Choices) > 0 {
		text = in.Choices[0].Message.Content
		for _, tc := range in.Choices[0].Message.ToolCalls {
			calls = append(calls, struct {
				ID        string
				Name      string
				Arguments string
			}{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
		}
	}
	id := in.ID
	if id == "" {
		id = fmt.Sprintf("resp_%d", time.Now().UnixNano())
	}
	if in.Model != "" {
		model = in.Model
	}
	var output []responsesOutMsg
	for _, tc := range calls {
		output = append(output, responsesOutMsg{
			Type:      "function_call",
			CallID:    tc.ID,
			Name:      tc.Name,
			Arguments: tc.Arguments,
		})
	}
	if text != "" || len(output) == 0 {
		output = append(output, responsesOutMsg{
			Type: "message",
			Role: "assistant",
			Content: []responsesOutPart{{
				Type: "output_text",
				Text: text,
			}},
		})
	}
	out := responsesOutput{
		ID:         id,
		Object:     "response",
		Status:     "completed",
		Model:      model,
		Output:     output,
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
