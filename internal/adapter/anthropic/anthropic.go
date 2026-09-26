package anthropic

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
	"github.com/ks1686/peaproxy/internal/translate"
)

const (
	Name           = "anthropic"
	DefaultBaseURL = "https://api.anthropic.com"
	APIVersion     = "2023-06-01"
)

// Adapter is a first-class Anthropic API-key client (Messages API).
type Adapter struct {
	id      string
	baseURL string
	apiKey  string
	client  *http.Client
}

// New uses https://api.anthropic.com unless BaseURL is set.
func New(opts adapter.Options) (adapter.Adapter, error) {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	base = strings.TrimSuffix(base, "/v1")
	id := opts.ID
	if id == "" {
		id = Name
	}
	return &Adapter{
		id:      id,
		baseURL: base,
		apiKey:  opts.APIKey,
		client:  adapter.HTTPClient(0, opts.ObserveHeaders),
	}, nil
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		Chat: true, Stream: true, VisionIn: true, Tools: true,
		ListModels: true, APIKey: a.apiKey != "",
	}
}

func (a *Adapter) Validate(ctx context.Context) error {
	_, err := a.ListModels(ctx)
	return err
}

func (a *Adapter) ListModels(ctx context.Context) ([]catalog.Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	a.headers(req)
	c := *a.client
	c.Timeout = 8 * time.Second
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic list models: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, adapter.HTTPError{Status: resp.StatusCode, Body: truncate(body)}
	}
	var list struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	out := make([]catalog.Model, 0, len(list.Data))
	for _, m := range list.Data {
		out = append(out, catalog.Model{
			ID:          m.ID,
			DisplayName: m.DisplayName,
			Provider:    Name,
			AccountID:   a.id,
			Tier:        catalog.TierPaid,
			Modalities:  catalog.InferModalities(m.ID),
			Status:      "ready",
			Exposed:     true,
			Routable:    true,
		})
	}
	return out, nil
}

func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	raw, err := a.claudeBody(req, false)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	out, err := a.Messages(ctx, raw)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	oa, err := translate.FromClaude(out)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	return adapter.ChatResponse{Model: req.Model, Raw: oa, Content: translate.ClaudeText(out)}, nil
}

func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	raw, err := a.claudeBody(req, true)
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- translate.ClaudeSSEToOpenAI(pr, w)
		_ = pr.Close()
	}()
	err = a.MessagesStream(ctx, raw, pw)
	_ = pw.Close()
	convErr := <-errCh
	if err != nil {
		return err
	}
	return convErr
}

func (a *Adapter) Messages(ctx context.Context, raw []byte) ([]byte, error) {
	raw = jsonx.SetStream(raw, false)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	a.headers(httpReq)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, adapter.HTTPError{Status: resp.StatusCode, Body: truncate(body)}
	}
	return body, nil
}

func (a *Adapter) MessagesStream(ctx context.Context, raw []byte, w io.Writer) error {
	raw = jsonx.SetStream(raw, true)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	a.headers(httpReq)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return adapter.HTTPError{Status: resp.StatusCode, Body: truncate(body)}
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (a *Adapter) claudeBody(req adapter.ChatRequest, stream bool) ([]byte, error) {
	if len(req.Raw) > 0 && translate.LooksLikeClaude(req.Raw) {
		return jsonx.SetStream(req.Raw, stream), nil
	}
	if len(req.Raw) > 0 {
		return translate.ToClaude(req.Raw, stream)
	}
	oa, err := json.Marshal(struct {
		Model    string            `json:"model"`
		Messages []adapter.Message `json:"messages"`
		Stream   bool              `json:"stream"`
	}{Model: req.Model, Messages: req.Messages, Stream: stream})
	if err != nil {
		return nil, err
	}
	return translate.ToClaude(oa, stream)
}

func (a *Adapter) headers(req *http.Request) {
	req.Header.Set("anthropic-version", APIVersion)
	if a.apiKey != "" {
		req.Header.Set("x-api-key", a.apiKey)
	}
}

func truncate(b []byte) string {
	const n = 240
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.NativeMessages = (*Adapter)(nil)
