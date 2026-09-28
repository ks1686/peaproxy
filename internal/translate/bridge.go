package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// LooksLikeClaude reports whether raw is an Anthropic Messages request.
func LooksLikeClaude(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	if !bytes.Contains(raw, []byte(`"max_tokens"`)) {
		return false
	}
	if bytes.Contains(raw, []byte(`"image_url"`)) {
		return false
	}
	var p struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return false
	}
	if len(p.System) > 0 && string(p.System) != "null" {
		return true
	}
	for _, m := range p.Messages {
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "image" || b.Type == "tool_use" || b.Type == "tool_result" {
				return true
			}
		}
	}
	return false
}

type claudePost struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []claudePostMsg `json:"messages"`
	System    string          `json:"system,omitempty"`
	Stream    bool            `json:"stream"`
	Tools     json.RawMessage `json:"tools,omitempty"`
}

type claudePostMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type openAIIn struct {
	Model     string          `json:"model"`
	Stream    bool            `json:"stream"`
	MaxTokens int             `json:"max_tokens"`
	Tools     json.RawMessage `json:"tools"`
	Messages  []openAIInMsg   `json:"messages"`
}

type openAIInMsg struct {
	Role            string           `json:"role"`
	Content         json.RawMessage  `json:"content"`
	ToolCalls       []openAIToolCall `json:"tool_calls"`
	ToolCallID      string           `json:"tool_call_id"`
	ReasoningOpaque json.RawMessage  `json:"reasoning_opaque"`
}

// CanonicalClaudeModel strips a provider prefix (anthropic/…) and rewrites
// dotted version aliases (claude-sonnet-4.5 → claude-sonnet-4-5) that harnesses
// send and Anthropic OAuth rejects. Live ListModels remains the catalog source
// of truth; this is wire-id cleanup, not an allowlist.
func CanonicalClaudeModel(model string) string {
	model = strings.TrimSpace(model)
	if i := strings.LastIndexByte(model, '/'); i >= 0 {
		model = model[i+1:]
	}
	model = strings.ToLower(model)
	return strings.ReplaceAll(model, ".", "-")
}

// ToClaude converts an OpenAI chat.completions request into Anthropic Messages JSON (structs).
func ToClaude(raw []byte, stream bool) ([]byte, error) {
	var in openAIIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	maxTok := in.MaxTokens
	if maxTok <= 0 {
		maxTok = 4096
	}
	out := claudePost{Model: CanonicalClaudeModel(in.Model), MaxTokens: maxTok, Stream: stream}
	if len(in.Tools) > 0 && string(in.Tools) != "null" {
		converted, err := toolsToClaude(in.Tools)
		if err != nil {
			return nil, err
		}
		out.Tools = converted
	}
	var pendingResults []claudeToolResultBlock
	flushResults := func() error {
		if len(pendingResults) == 0 {
			return nil
		}
		raw, err := json.Marshal(pendingResults)
		if err != nil {
			return err
		}
		out.Messages = append(out.Messages, claudePostMsg{Role: "user", Content: raw})
		pendingResults = nil
		return nil
	}
	for _, m := range in.Messages {
		role := strings.ToLower(m.Role)
		switch role {
		case "system":
			text, _ := contentText(m.Content)
			if out.System != "" {
				out.System += "\n"
			}
			out.System += text
		case "tool":
			pendingResults = append(pendingResults, claudeToolResultBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   toolResultText(m.Content),
			})
		case "assistant":
			if err := flushResults(); err != nil {
				return nil, err
			}
			content, err := assistantOpenAIToClaude(m)
			if err != nil {
				return nil, err
			}
			out.Messages = append(out.Messages, claudePostMsg{Role: "assistant", Content: content})
		case "user":
			if err := flushResults(); err != nil {
				return nil, err
			}
			content, err := openAIContentToClaude(m.Content)
			if err != nil {
				return nil, err
			}
			out.Messages = append(out.Messages, claudePostMsg{Role: role, Content: content})
		default:
			// skip unknown roles rather than invent Claude turns
		}
	}
	if err := flushResults(); err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

type claudeToolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type claudeToolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

func assistantOpenAIToClaude(m openAIInMsg) (json.RawMessage, error) {
	entries, err := parseOpaque(m.ReasoningOpaque)
	if err != nil {
		return nil, err
	}
	blocks := make([]any, 0, 1+len(m.ToolCalls)+len(entries))
	for _, e := range entries {
		switch e.Kind {
		case kindAnthropicThinking:
			blocks = append(blocks, struct {
				Type      string `json:"type"`
				Thinking  string `json:"thinking"`
				Signature string `json:"signature,omitempty"`
			}{Type: "thinking", Thinking: e.Thinking, Signature: e.Signature})
		case kindAnthropicRedacted:
			blocks = append(blocks, struct {
				Type string `json:"type"`
				Data string `json:"data"`
			}{Type: "redacted_thinking", Data: e.Data})
		}
	}
	text, _ := contentText(m.Content)
	if text != "" {
		blocks = append(blocks, claudeContent{Type: "text", Text: text})
	}
	for _, tc := range m.ToolCalls {
		blocks = append(blocks, claudeToolUseBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: toolUseInput(tc.Function.Arguments),
		})
	}
	if len(blocks) == 0 {
		return json.Marshal([]claudeContent{{Type: "text", Text: ""}})
	}
	return json.Marshal(blocks)
}

func toolsToClaude(raw json.RawMessage) (json.RawMessage, error) {
	var tools []openAITool
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, fmt.Errorf("openai tools: %w", err)
	}
	out := make([]claudeTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, claudeTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Strict:      t.Function.Strict,
			InputSchema: t.Function.Parameters,
		})
	}
	return json.Marshal(out)
}

func openAIContentToClaude(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return json.Marshal("")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return json.Marshal([]claudeContent{{Type: "text", Text: s}})
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("openai content: %w", err)
	}
	type claudeImg struct {
		Type   string `json:"type"`
		Source struct {
			Type      string `json:"type"`
			URL       string `json:"url,omitempty"`
			MediaType string `json:"media_type,omitempty"`
			Data      string `json:"data,omitempty"`
		} `json:"source"`
	}
	blocks := make([]any, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text":
			blocks = append(blocks, claudeContent{Type: "text", Text: p.Text})
		case "image_url":
			if p.ImageURL == nil || p.ImageURL.URL == "" {
				return nil, fmt.Errorf("image_url missing url")
			}
			img := claudeImg{Type: "image"}
			if u := p.ImageURL.URL; strings.HasPrefix(u, "data:") {
				mediaType, data := parseDataURL(u)
				img.Source.Type = "base64"
				img.Source.MediaType = mediaType
				img.Source.Data = data
			} else {
				img.Source.Type = "url"
				img.Source.URL = p.ImageURL.URL
			}
			blocks = append(blocks, img)
		default:
			return nil, fmt.Errorf("refusing to drop OpenAI content part %q", p.Type)
		}
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("empty multimodal content")
	}
	return json.Marshal(blocks)
}

type claudeNative struct {
	ID         string              `json:"id"`
	Model      string              `json:"model"`
	Content    []claudeNativeBlock `json:"content"`
	StopReason string              `json:"stop_reason"`
	Usage      *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type claudeNativeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	Data      string          `json:"data"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type openAIOut struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role            string           `json:"role"`
			Content         json.RawMessage  `json:"content"`
			ToolCalls       []openAIToolCall `json:"tool_calls,omitempty"`
			ReasoningOpaque json.RawMessage  `json:"reasoning_opaque,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

// FromClaude maps a Messages response to a chat.completion object.
func FromClaude(raw []byte) ([]byte, error) {
	var in claudeNative
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	text := ClaudeText(raw)
	var calls []openAIToolCall
	var opaque []opaqueEntry
	for _, c := range in.Content {
		switch c.Type {
		case "tool_use":
			tc := openAIToolCall{ID: c.ID, Type: "function"}
			tc.Function.Name = c.Name
			tc.Function.Arguments = toolUseArguments(c.Input)
			calls = append(calls, tc)
		case "thinking":
			opaque = append(opaque, opaqueEntry{Kind: kindAnthropicThinking, Thinking: c.Thinking, Signature: c.Signature})
		case "redacted_thinking":
			opaque = append(opaque, opaqueEntry{Kind: kindAnthropicRedacted, Data: c.Data})
		}
	}
	opaqueRaw, err := marshalOpaque(opaque)
	if err != nil {
		return nil, err
	}
	var content json.RawMessage
	if len(calls) > 0 {
		content, err = assistantOpenAIContent(text)
	} else {
		content, err = json.Marshal(text)
	}
	if err != nil {
		return nil, err
	}
	out := openAIOut{ID: in.ID, Object: "chat.completion", Model: in.Model}
	out.Choices = make([]struct {
		Index   int `json:"index"`
		Message struct {
			Role            string           `json:"role"`
			Content         json.RawMessage  `json:"content"`
			ToolCalls       []openAIToolCall `json:"tool_calls,omitempty"`
			ReasoningOpaque json.RawMessage  `json:"reasoning_opaque,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	}, 1)
	out.Choices[0].Message.Role = "assistant"
	out.Choices[0].Message.Content = content
	out.Choices[0].Message.ToolCalls = calls
	out.Choices[0].Message.ReasoningOpaque = opaqueRaw
	out.Choices[0].FinishReason = openAIChatFinishReason(len(calls) > 0 || in.StopReason == "tool_use")
	if in.StopReason == "max_tokens" && len(calls) == 0 {
		out.Choices[0].FinishReason = "length"
	}
	if in.Usage != nil {
		out.Usage = &struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		}{PromptTokens: in.Usage.InputTokens, CompletionTokens: in.Usage.OutputTokens}
	}
	return json.Marshal(out)
}

// ClaudeText extracts concatenated text blocks.
func ClaudeText(raw []byte) string {
	var in claudeNative
	if err := json.Unmarshal(raw, &in); err != nil {
		return ""
	}
	var b bytes.Buffer
	for _, c := range in.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func parseDataURL(u string) (mediaType, data string) {
	mediaType = "image/png"
	data = u
	rest, ok := strings.CutPrefix(u, "data:")
	if !ok {
		return mediaType, data
	}
	header, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return mediaType, rest
	}
	data = payload
	if mt, _, cut := strings.Cut(header, ";"); cut && mt != "" {
		mediaType = mt
	} else if header != "" && !strings.Contains(header, "base64") {
		mediaType = header
	}
	return mediaType, data
}
