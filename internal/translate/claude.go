// Package translate converts Anthropic Messages JSON to OpenAI chat completions
// using structs (stable key order) — never map[string]any.
package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/jsonx"
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
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

type openAIResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type claudeResponse struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	Content    []claudeContent `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      claudeUsage     `json:"usage"`
}

type claudeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
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
	msgs := make([]openAIMessage, 0, len(in.Messages)+1)
	if sys := systemText(in.System); sys != "" {
		rawSys, err := json.Marshal(sys)
		if err != nil {
			return nil, adapter.ChatRequest{}, err
		}
		msgs = append(msgs, openAIMessage{Role: "system", Content: rawSys})
	}
	for _, m := range in.Messages {
		content, err := claudeContentToOpenAI(m.Content)
		if err != nil {
			return nil, adapter.ChatRequest{}, err
		}
		role := m.Role
		if role == "human" {
			role = "user"
		}
		msgs = append(msgs, openAIMessage{Role: role, Content: content})
	}
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
	text := ""
	if len(in.Choices) > 0 {
		text = in.Choices[0].Message.Content
	}
	id := in.ID
	if id == "" {
		id = fmt.Sprintf("msg_%d", time.Now().UnixNano())
	}
	if in.Model != "" {
		model = in.Model
	}
	stop := "end_turn"
	if len(in.Choices) > 0 && in.Choices[0].FinishReason == "length" {
		stop = "max_tokens"
	}
	out := claudeResponse{
		ID:         id,
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		Content:    []claudeContent{{Type: "text", Text: text}},
		StopReason: stop,
	}
	if in.Usage != nil {
		out.Usage = claudeUsage{InputTokens: in.Usage.PromptTokens, OutputTokens: in.Usage.CompletionTokens}
	}
	return json.Marshal(out)
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

func claudeContentToOpenAI(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return json.Marshal("")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return json.Marshal(s)
	}
	var blocks []struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		Source *struct {
			Type      string `json:"type"`
			URL       string `json:"url"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("unsupported Claude content: %w", err)
	}
	var parts []oaContentPart
	var textOnly strings.Builder
	hasImage := false
	for _, bl := range blocks {
		switch bl.Type {
		case "text", "":
			textOnly.WriteString(bl.Text)
			parts = append(parts, oaContentPart{Type: "text", Text: bl.Text})
		case "image":
			hasImage = true
			url, err := claudeImageURL(bl.Source)
			if err != nil {
				return nil, err
			}
			parts = append(parts, oaContentPart{Type: "image_url", ImageURL: &oaImagePart{URL: url}})
		default:
			if textOnly.Len() == 0 && !hasImage {
				return nil, fmt.Errorf("refusing to drop non-text Claude content into an empty message")
			}
		}
	}
	if hasImage {
		if len(parts) == 0 {
			return nil, fmt.Errorf("empty multimodal content")
		}
		return json.Marshal(parts)
	}
	return json.Marshal(textOnly.String())
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
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
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
		item := openAITool{Type: "function"}
		item.Function.Name = t.Name
		item.Function.Description = t.Description
		item.Function.Parameters = t.InputSchema
		out = append(out, item)
	}
	return json.Marshal(out)
}
