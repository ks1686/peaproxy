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
	"github.com/ks1686/peaproxy/internal/adapter/openai_oauth"
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

func (a *Adapter) postResponse(ctx context.Context, path string, body []byte) (*http.Response, error) {
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
	if resp.StatusCode >= 300 {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		return nil, adapter.NewHTTPError(resp, truncate(raw))
	}
	return resp, nil
}

func (a *Adapter) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	resp, err := a.postResponse(ctx, path, body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
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
		rb, err := openai_oauth.ChatToResponses(chat, req.Model, true)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
		raw, err := a.post(ctx, "/responses", rb)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
		body, err := openai_oauth.ResponsesStreamToJSON(raw)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
		oa, content, err := openai_oauth.ResponsesToChatCompletion(req.Model, body)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
		return adapter.ChatResponse{Model: req.Model, Raw: oa, Content: content}, nil
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

// streamViaWire preserves upstream SSE and converts it to the Chat Completions
// stream consumed by the gateway. Tool/reasoning semantics are handled by the
// existing Responses and Messages translators rather than a local parser.
func (a *Adapter) streamViaWire(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	chat, err := chatBody(req)
	if err != nil {
		return err
	}
	switch wireOf(req.Model) {
	case wireResponses:
		rb, err := openai_oauth.ChatToResponses(chat, req.Model, true)
		if err != nil {
			return err
		}
		resp, err := a.postResponse(ctx, "/responses", rb)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		return openai_oauth.ResponsesSSEToOpenAI(resp.Body, w, req.Model)
	case wireMessages:
		mb, err := translate.ToClaude(chat, true)
		if err != nil {
			return err
		}
		resp, err := a.postResponse(ctx, "/v1/messages", mb)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		return translate.ClaudeSSEToOpenAI(resp.Body, w)
	default:
		return fmt.Errorf("copilot_oauth: unsupported stream wire")
	}
}
