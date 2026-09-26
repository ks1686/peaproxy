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
}

type claudePostMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type openAIIn struct {
	Model     string `json:"model"`
	Stream    bool   `json:"stream"`
	MaxTokens int    `json:"max_tokens"`
	Messages  []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
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
	out := claudePost{Model: in.Model, MaxTokens: maxTok, Stream: stream}
	for _, m := range in.Messages {
		if m.Role == "system" {
			text, _ := contentText(m.Content)
			if out.System != "" {
				out.System += "\n"
			}
			out.System += text
			continue
		}
		role := m.Role
		if role == "assistant" || role == "user" {
			content, err := openAIContentToClaude(m.Content)
			if err != nil {
				return nil, err
			}
			out.Messages = append(out.Messages, claudePostMsg{Role: role, Content: content})
		}
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
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type openAIOut struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
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
	stop := "stop"
	if in.StopReason == "max_tokens" {
		stop = "length"
	}
	out := openAIOut{ID: in.ID, Object: "chat.completion", Model: in.Model}
	out.Choices = make([]struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	}, 1)
	out.Choices[0].Message.Role = "assistant"
	out.Choices[0].Message.Content = text
	out.Choices[0].FinishReason = stop
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
