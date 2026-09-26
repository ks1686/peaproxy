package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

const (
	Name           = "ollama"
	DefaultBaseURL = "http://127.0.0.1:11434/v1"
)

// Adapter talks to a local Ollama OpenAI-compatible server.
type Adapter struct {
	id      string
	baseURL string
	client  *http.Client
}

// New constructs an Ollama adapter. ListModels hits the live /v1/models (or /api/tags later).
func New(opts adapter.Options) (adapter.Adapter, error) {
	base := opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	id := opts.ID
	if id == "" {
		id = Name
	}
	return &Adapter{
		id:      id,
		baseURL: strings.TrimRight(base, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		Chat:       true,
		Stream:     true,
		VisionIn:   true,
		ListModels: true,
		Local:      true,
	}
}

func (a *Adapter) Validate(ctx context.Context) error {
	_, err := a.ListModels(ctx)
	return err
}

func (a *Adapter) ListModels(ctx context.Context) ([]catalog.Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama list models: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ollama list models: HTTP %d: %s", resp.StatusCode, truncate(body))
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	out := make([]catalog.Model, 0, len(list.Data))
	for _, m := range list.Data {
		out = append(out, catalog.Model{
			ID:        m.ID,
			Provider:  Name,
			AccountID: a.id,
			Tier:      catalog.TierLocal,
			Status:    "ready",
		})
	}
	return out, nil
}

func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	raw := req.Raw
	if len(raw) == 0 {
		payload := map[string]any{
			"model":    req.Model,
			"messages": toOpenAIMessages(req.Messages),
			"stream":   false,
		}
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return adapter.ChatResponse{}, fmt.Errorf("ollama chat: HTTP %d: %s", resp.StatusCode, truncate(body))
	}
	return adapter.ChatResponse{Model: req.Model, Raw: body, Content: extractContent(body)}, nil
}

func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	_ = ctx
	_ = req
	_ = w
	return adapter.ErrNotImplemented
}

func toOpenAIMessages(msgs []adapter.Message) []map[string]string {
	out := make([]map[string]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]string{"role": m.Role, "content": m.Content})
	}
	return out
}

func extractContent(body []byte) string {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Choices) == 0 {
		return ""
	}
	return parsed.Choices[0].Message.Content
}

func truncate(b []byte) string {
	const n = 240
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
