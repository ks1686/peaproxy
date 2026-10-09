package copilot_oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/translate"
)

// wireFor picks the upstream endpoint Copilot accepts for a model. Copilot
// refuses chat/completions for Responses-only models with
// unsupported_api_for_model, so the adapter routes by family rather than
// forwarding every id to the chat endpoint.
type wireFor int

const (
	wireChat wireFor = iota
	wireResponses
	wireMessages
)

func wireOf(model string) wireFor {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "claude-"):
		return wireMessages
	case strings.HasPrefix(m, "gpt-"), strings.HasPrefix(m, "grok-"), strings.HasPrefix(m, "mai-code"):
		return wireResponses
	default:
		return wireChat
	}
}

// chatBody returns the chat-completions body for the request. translate
// produces the Responses and Messages shapes from the same messages.
func chatBody(req adapter.ChatRequest) ([]byte, error) {
	if len(req.Raw) > 0 {
		return req.Raw, nil
	}
	return json.Marshal(struct {
		Model    string            `json:"model"`
		Messages []adapter.Message `json:"messages"`
		Stream   bool              `json:"stream"`
	}{Model: req.Model, Messages: req.Messages, Stream: req.Stream})
}

// responsesBody converts chat messages into the Responses input shape.
func responsesBody(model string, chat []byte) ([]byte, error) {
	var in struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(chat, &in); err != nil {
		return nil, err
	}
	var instructions []string
	var input []map[string]any
	for _, m := range in.Messages {
		text := contentText(m.Content)
		if m.Role == "system" || m.Role == "developer" {
			instructions = append(instructions, text)
			continue
		}
		input = append(input, map[string]any{
			"type": "message", "role": m.Role,
			"content": []map[string]string{{"type": "input_text", "text": text}},
		})
	}
	body := map[string]any{"model": model, "input": input, "stream": true, "store": false}
	if len(instructions) > 0 {
		body["instructions"] = strings.Join(instructions, "\n\n")
	}
	return json.Marshal(body)
}

// contentText flattens a chat content field (string or parts) to text.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" || p.Type == "input_text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

func (a *Adapter) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	a.mu.Lock()
	tok := a.token
	base := a.apiBase
	a.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	a.setCopilotHeaders(req)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, adapter.NewHTTPError(resp, truncate(raw))
	}
	return raw, nil
}

// chatViaWire sends a non-streaming chat to the endpoint the model needs and
// returns the chat-completions JSON the gateway expects.
func (a *Adapter) chatViaWire(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	chat, err := chatBody(req)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	switch wireOf(req.Model) {
	case wireResponses:
		rb, err := responsesBody(req.Model, chat)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
		raw, err := a.post(ctx, "/responses", rb)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
		text := responsesStreamText(raw)
		oa, err := translate.FromChatContent("copilot", req.Model, text)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
		return adapter.ChatResponse{Model: req.Model, Raw: oa, Content: text}, nil
	case wireMessages:
		mb, err := translate.ToClaude(chat, false)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
		raw, err := a.post(ctx, "/v1/messages", mb)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
		oa, err := translate.FromClaude(raw)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
		return adapter.ChatResponse{Model: req.Model, Raw: oa, Content: translate.ClaudeText(raw)}, nil
	default:
		return adapter.ChatResponse{}, errChatWire
	}
}

var errChatWire = fmt.Errorf("copilot_oauth: chat wire")

// responsesStreamText reads the completed text from a Responses SSE body. It
// prefers the final response.completed payload and falls back to the deltas.
func responsesStreamText(raw []byte) string {
	var deltas strings.Builder
	var final string
	for _, line := range strings.Split(string(raw), "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal([]byte(payload), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.output_text.delta":
			deltas.WriteString(ev.Delta)
		case "response.completed":
			final = responsesText(ev.Response)
		}
	}
	if final != "" {
		return final
	}
	if deltas.Len() > 0 {
		return deltas.String()
	}
	return responsesText(raw)
}

// responsesText collects output_text parts from a Responses body.
func responsesText(raw []byte) string {
	var out struct {
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	var b strings.Builder
	for _, o := range out.Output {
		for _, c := range o.Content {
			if c.Type == "output_text" {
				b.WriteString(c.Text)
			}
		}
	}
	return b.String()
}
