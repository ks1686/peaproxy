// Package translate converts Anthropic Messages JSON to OpenAI chat completions
// using structs (stable key order) — never map[string]any.
package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/sizehint"
)

// ClaudeRequest is a subset of POST /v1/messages.
type ClaudeRequest struct {
	Model     string          `json:"model"`
	Messages  []ClaudeMessage `json:"messages"`
	System    json.RawMessage `json:"system"`
	MaxTokens int             `json:"max_tokens"`
	Stream    bool            `json:"stream"`
	Tools     json.RawMessage `json:"tools"`
}

// ClaudeMessage is one Anthropic turn. Content may be a string or a block list.
type ClaudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type openAIRequest struct {
	Model      string          `json:"model"`
	Messages   []openAIMessage `json:"messages"`
	Stream     bool            `json:"stream"`
	MaxTokens  int             `json:"max_tokens,omitempty"`
	Tools      json.RawMessage `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
}

type openAIMessage struct {
	Role            string          `json:"role"`
	Content         json.RawMessage `json:"content,omitempty"`
	ToolCalls       json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID      string          `json:"tool_call_id,omitempty"`
	Name            string          `json:"name,omitempty"`
	ReasoningOpaque json.RawMessage `json:"reasoning_opaque,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role            string           `json:"role"`
			Content         string           `json:"content"`
			ToolCalls       []openAIToolCall `json:"tool_calls"`
			ReasoningOpaque json.RawMessage  `json:"reasoning_opaque"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type claudeResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Model      string             `json:"model"`
	Content    []claudeContentOut `json:"content"`
	StopReason string             `json:"stop_reason"`
	Usage      claudeUsage        `json:"usage"`
}

type claudeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeContentOut struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ToOpenAI converts an Anthropic body to an OpenAI chat.completions body (structs).
func ToOpenAI(raw []byte) ([]byte, adapter.ChatRequest, error) {
	var in ClaudeRequest
	if err := jsonx.DecodeStrict(raw, &in); err != nil {
		return nil, adapter.ChatRequest{}, err
	}
	if in.Model == "" {
		return nil, adapter.ChatRequest{}, fmt.Errorf("missing model")
	}
	msgs := make([]openAIMessage, 0, sizehint.Sum(len(in.Messages), 1))
	if sys := systemText(in.System); sys != "" {
		rawSys, err := json.Marshal(sys)
		if err != nil {
			return nil, adapter.ChatRequest{}, err
		}
		msgs = append(msgs, openAIMessage{Role: "system", Content: rawSys})
	}
	converted, err := claudeMessagesToOpenAI(in.Messages)
	if err != nil {
		return nil, adapter.ChatRequest{}, err
	}
	msgs = append(msgs, converted...)
	out := openAIRequest{
		Model:     in.Model,
		Messages:  msgs,
		Stream:    in.Stream,
		MaxTokens: in.MaxTokens,
	}
	if len(in.Tools) > 0 && string(in.Tools) != "null" {
		converted, err := toolsToOpenAI(in.Tools)
		if err != nil {
			return nil, adapter.ChatRequest{}, err
		}
		out.Tools = converted
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, adapter.ChatRequest{}, err
	}
	chatMsgs := make([]adapter.Message, 0, len(msgs))
	for _, m := range msgs {
		text, _ := contentText(m.Content)
		chatMsgs = append(chatMsgs, adapter.Message{Role: m.Role, Content: text})
	}
	return body, adapter.ChatRequest{Model: in.Model, Messages: chatMsgs, Stream: in.Stream, Raw: body}, nil
}

// FromOpenAI maps a chat.completion JSON object to Anthropic messages JSON.
func FromOpenAI(raw []byte, model string) ([]byte, error) {
	var in openAIResponse
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	var text string
	var calls []openAIToolCall
	var opaque []opaqueEntry
	finish := ""
	if len(in.Choices) > 0 {
		text = in.Choices[0].Message.Content
		calls = in.Choices[0].Message.ToolCalls
		finish = in.Choices[0].FinishReason
		parsed, err := parseOpaque(in.Choices[0].Message.ReasoningOpaque)
		if err != nil {
			return nil, err
		}
		opaque = parsed
	}
	id := in.ID
	if id == "" {
		id = fmt.Sprintf("msg_%d", time.Now().UnixNano())
	}
	if in.Model != "" {
		model = in.Model
	}
	content := make([]claudeContentOut, 0, sizehint.Sum(1, len(calls), len(opaque)))
	for _, e := range opaque {
		switch e.Kind {
		case kindAnthropicThinking:
			content = append(content, claudeContentOut{Type: "thinking", Thinking: e.Thinking, Signature: e.Signature})
		case kindAnthropicRedacted:
			content = append(content, claudeContentOut{Type: "redacted_thinking", Data: e.Data})
		}
	}
	if text != "" || (len(calls) == 0 && len(content) == 0) {
		content = append(content, claudeContentOut{Type: "text", Text: text})
	}
	for _, tc := range calls {
		content = append(content, claudeContentOut{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: toolUseInput(tc.Function.Arguments),
		})
	}
	out := claudeResponse{
		ID:         id,
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		Content:    content,
		StopReason: claudeStopReason(finish, len(calls) > 0),
	}
	if in.Usage != nil {
		out.Usage = claudeUsage{InputTokens: in.Usage.PromptTokens, OutputTokens: in.Usage.CompletionTokens}
	}
	return json.Marshal(out)
}

// claudeStopReason reports truncation and refusal before tool use: a turn cut
// off at the token limit or stopped by a content filter may end inside a tool
// call, and clients must not run that call.
func claudeStopReason(finish string, hadToolCalls bool) string {
	if finish == "length" {
		return "max_tokens"
	}
	if finish == "content_filter" {
		return "refusal"
	}
	if hadToolCalls || finish == "tool_calls" {
		return "tool_use"
	}
	return "end_turn"
}

func toolUseInput(arguments string) json.RawMessage {
	raw := bytes.TrimSpace([]byte(arguments))
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(raw)
}

func toolUseArguments(input json.RawMessage) string {
	input = bytes.TrimSpace(input)
	if len(input) == 0 || string(input) == "null" {
		return "{}"
	}
	return string(input)
}

func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	text, err := contentText(raw)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return text
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	Data      string          `json:"data"`
	Source    *struct {
		Type      string `json:"type"`
		URL       string `json:"url"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

func claudeMessagesToOpenAI(in []ClaudeMessage) ([]openAIMessage, error) {
	out := make([]openAIMessage, 0, len(in))
	for _, m := range in {
		role := m.Role
		if role == "human" {
			role = "user"
		}
		msgs, err := claudeMessageToOpenAI(role, m.Content)
		if err != nil {
			return nil, err
		}
		out = append(out, msgs...)
	}
	return out, nil
}

func claudeMessageToOpenAI(role string, raw json.RawMessage) ([]openAIMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		content, err := json.Marshal("")
		if err != nil {
			return nil, err
		}
		return []openAIMessage{{Role: role, Content: content}}, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		content, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		return []openAIMessage{{Role: role, Content: content}}, nil
	}
	var blocks []claudeBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("unsupported Claude content: %w", err)
	}
	var text strings.Builder
	var parts []oaContentPart
	var calls []openAIToolCall
	var results []openAIMessage
	var opaque []opaqueEntry
	hasImage := false
	hasUnknown := false
	for _, bl := range blocks {
		switch bl.Type {
		case "text", "":
			text.WriteString(bl.Text)
			parts = append(parts, oaContentPart{Type: "text", Text: bl.Text})
		case "thinking":
			if role != "assistant" {
				hasUnknown = true
				break
			}
			opaque = append(opaque, opaqueEntry{Kind: kindAnthropicThinking, Thinking: bl.Thinking, Signature: bl.Signature})
		case "redacted_thinking":
			if role != "assistant" {
				hasUnknown = true
				break
			}
			opaque = append(opaque, opaqueEntry{Kind: kindAnthropicRedacted, Data: bl.Data})
		case "image":
			hasImage = true
			url, err := claudeImageURL(bl.Source)
			if err != nil {
				return nil, err
			}
			parts = append(parts, oaContentPart{Type: "image_url", ImageURL: &oaImagePart{URL: url}})
		case "tool_use":
			tc := openAIToolCall{ID: bl.ID, Type: "function"}
			tc.Function.Name = bl.Name
			tc.Function.Arguments = toolUseArguments(bl.Input)
			calls = append(calls, tc)
		case "tool_result":
			content, err := json.Marshal(toolResultText(bl.Content))
			if err != nil {
				return nil, err
			}
			results = append(results, openAIMessage{Role: "tool", Content: content, ToolCallID: bl.ToolUseID})
		default:
			hasUnknown = true
		}
	}
	if hasUnknown && text.Len() == 0 && !hasImage && len(calls) == 0 && len(results) == 0 && len(opaque) == 0 {
		return nil, fmt.Errorf("refusing to drop non-text Claude content into an empty message")
	}
	opaqueRaw, err := marshalOpaque(opaque)
	if err != nil {
		return nil, err
	}
	if len(results) > 0 {
		if text.Len() == 0 && !hasImage {
			return withOpaque(results, opaqueRaw), nil
		}
		userContent, err := openAIUserContent(text.String(), parts, hasImage)
		if err != nil {
			return nil, err
		}
		return withOpaque(append(results, openAIMessage{Role: "user", Content: userContent}), opaqueRaw), nil
	}
	if len(opaqueRaw) > 0 && text.Len() == 0 && !hasImage && len(calls) == 0 {
		return []openAIMessage{{Role: role, Content: json.RawMessage("null"), ReasoningOpaque: opaqueRaw}}, nil
	}
	if len(calls) > 0 {
		rawCalls, err := json.Marshal(calls)
		if err != nil {
			return nil, err
		}
		content, err := assistantOpenAIContent(text.String())
		if err != nil {
			return nil, err
		}
		return []openAIMessage{{Role: "assistant", Content: content, ToolCalls: rawCalls, ReasoningOpaque: opaqueRaw}}, nil
	}
	content, err := openAIUserContent(text.String(), parts, hasImage)
	if err != nil {
		return nil, err
	}
	return withOpaque([]openAIMessage{{Role: role, Content: content}}, opaqueRaw), nil
}

func assistantOpenAIContent(text string) (json.RawMessage, error) {
	if text == "" {
		return json.RawMessage("null"), nil
	}
	return json.Marshal(text)
}

func openAIUserContent(text string, parts []oaContentPart, hasImage bool) (json.RawMessage, error) {
	if hasImage {
		if len(parts) == 0 {
			return nil, fmt.Errorf("empty multimodal content")
		}
		return json.Marshal(parts)
	}
	return json.Marshal(text)
}

func systemText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	text, _ := contentText(raw)
	return text
}

func contentText(raw json.RawMessage) (string, error) {
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
		return "", fmt.Errorf("unsupported Claude content: %w", err)
	}
	var b strings.Builder
	hasNonText := false
	for _, bl := range blocks {
		switch bl.Type {
		case "text", "":
			b.WriteString(bl.Text)
		default:
			hasNonText = true
		}
	}
	if hasNonText && b.Len() == 0 {
		return "", fmt.Errorf("refusing to drop non-text Claude content into an empty message")
	}
	return b.String(), nil
}

type oaImagePart struct {
	URL string `json:"url"`
}

type oaContentPart struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	ImageURL *oaImagePart `json:"image_url,omitempty"`
}

func claudeImageURL(src *struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}) (string, error) {
	if src == nil {
		return "", fmt.Errorf("claude image missing source")
	}
	switch src.Type {
	case "url":
		if src.URL == "" {
			return "", fmt.Errorf("claude image url empty")
		}
		return src.URL, nil
	case "base64":
		if src.Data == "" {
			return "", fmt.Errorf("claude image base64 empty")
		}
		if strings.HasPrefix(src.Data, "data:") {
			return src.Data, nil
		}
		mt := src.MediaType
		if mt == "" {
			mt = "image/png"
		}
		return "data:" + mt + ";base64," + src.Data, nil
	default:
		return "", fmt.Errorf("unsupported claude image source %q", src.Type)
	}
}

type claudeTool struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Strict      *bool           `json:"strict,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Strict      *bool           `json:"strict,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func toolsToOpenAI(raw json.RawMessage) (json.RawMessage, error) {
	var tools []claudeTool
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, fmt.Errorf("claude tools: %w", err)
	}
	out := make([]openAITool, 0, len(tools))
	for _, t := range tools {
		if t.Type != "" && t.Type != "custom" {
			return nil, fmt.Errorf("unsupported built-in tool %q", t.Type)
		}
		item := openAITool{Type: "function"}
		item.Function.Name = t.Name
		item.Function.Description = t.Description
		item.Function.Strict = t.Strict
		item.Function.Parameters = t.InputSchema
		out = append(out, item)
	}
	return json.Marshal(out)
}
