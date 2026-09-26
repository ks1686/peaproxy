package openai_compat

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
	"github.com/ks1686/peaproxy/internal/jsonx"
)

const Name = "openai_compat"

// Adapter is a generic OpenAI-compatible HTTP client (LM Studio, llama.cpp, Groq, OpenRouter, …).
type Adapter struct {
	id           string
	baseURL      string
	apiKey       string
	sessionID    string
	tier         catalog.Tier
	client       *http.Client
	provider     string
	extraHeaders map[string]string
}

// New requires a base URL. The API key may be empty for local servers.
func New(opts adapter.Options) (adapter.Adapter, error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("openai_compat: baseURL is required")
	}
	id := opts.ID
	if id == "" {
		id = Name
	}
	tier := opts.Tier
	if tier == "" {
		tier = catalog.TierPaid
	}
	return &Adapter{
		id:           id,
		baseURL:      strings.TrimRight(opts.BaseURL, "/"),
		apiKey:       opts.APIKey,
		sessionID:    opts.SessionID,
		tier:         tier,
		client:       &http.Client{Timeout: 0},
		provider:     Name,
		extraHeaders: opts.ExtraHeaders,
	}, nil
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) SetProviderName(name string) { a.provider = name }

func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		Chat:       true,
		Stream:     true,
		VisionIn:   true,
		ImageOut:   false, // catalog may tag image_out; /v1/images/generations is not proxied yet
		ListModels: true,
		APIKey:     a.apiKey != "",
		Local:      a.tier == catalog.TierLocal,
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
	a.auth(req)
	client := a.client
	if client.Timeout == 0 {
		c := *a.client
		c.Timeout = 5 * time.Second
		client = &c
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s list models: %w", a.provider, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, adapter.HTTPError{Status: resp.StatusCode, Body: truncate(body)}
	}
	var list struct {
		Data []struct {
			ID           string `json:"id"`
			Architecture *struct {
				InputModalities  []string `json:"input_modalities"`
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	out := make([]catalog.Model, 0, len(list.Data))
	for _, m := range list.Data {
		var input, output []string
		if m.Architecture != nil {
			input = m.Architecture.InputModalities
			output = m.Architecture.OutputModalities
		}
		out = append(out, catalog.Model{
			ID:         m.ID,
			Provider:   a.provider,
			AccountID:  a.id,
			Tier:       inferTier(m.ID, a.tier),
			Modalities: catalog.ModalitiesFromLive(m.ID, input, output),
			Status:     "ready",
			Exposed:    true,
			Routable:   true,
		})
	}
	return out, nil
}

func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	raw, err := a.body(req, false)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	a.auth(httpReq)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return adapter.ChatResponse{}, adapter.HTTPError{Status: resp.StatusCode, Body: truncate(body)}
	}
	return adapter.ChatResponse{Model: req.Model, Raw: body, Content: extractContent(body)}, nil
}

func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	raw, err := a.body(req, true)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	a.auth(httpReq)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return adapter.HTTPError{Status: resp.StatusCode, Body: truncate(body)}
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (a *Adapter) body(req adapter.ChatRequest, stream bool) ([]byte, error) {
	if len(req.Raw) > 0 {
		return jsonx.SetStream(req.Raw, stream), nil
	}
	payload := chatRequest{
		Model:    req.Model,
		Messages: req.Messages,
		Stream:   stream,
	}
	return json.Marshal(payload)
}

type chatRequest struct {
	Model    string            `json:"model"`
	Messages []adapter.Message `json:"messages"`
	Stream   bool              `json:"stream"`
}

func (a *Adapter) auth(req *http.Request) {
	if a.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	if a.sessionID != "" {
		req.Header.Set("x-session-id", a.sessionID)
	}
	for k, v := range a.extraHeaders {
		if k != "" && v != "" {
			req.Header.Set(k, v)
		}
	}
}

func inferTier(id string, fallback catalog.Tier) catalog.Tier {
	lower := strings.ToLower(id)
	if strings.HasSuffix(lower, ":free") || strings.HasSuffix(lower, "-free") || strings.Contains(lower, "-free-") {
		return catalog.TierFree
	}
	return fallback
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
