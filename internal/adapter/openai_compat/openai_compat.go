package openai_compat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/translate"
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
		client:       adapter.HTTPClient(0, opts.ObserveHeaders),
		provider:     Name,
		extraHeaders: opts.ExtraHeaders,
	}, nil
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) SetProviderName(name string) { a.provider = name }

func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		Chat:  true,
		Tools: true,
		// Tools is declared because the adapter forwards the caller's `tools`
		// array untouched to an OpenAI-compatible endpoint, exactly as it
		// forwards VisionIn and ImageOut. Leaving this false was not a cautious
		// default: it silently excluded the whole OpenAI-compatible roster
		// from every tool-aware decision, including proxy-owned tools.
		Stream:     true,
		VisionIn:   true,
		ImageOut:   true,
		Embeddings: true,
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
	if a.provider == "workers_ai" {
		return a.listWorkersAIModels(ctx)
	}
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
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, adapter.NewHTTPError(resp, truncate(body))
	}
	var list struct {
		Data []struct {
			ID           string `json:"id"`
			Architecture *struct {
				InputModalities  []string `json:"input_modalities"`
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
			Pricing *struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
				// OpenRouter publishes cache rates for models that have them.
				// A model that does not omits them, and those components stay
				// unknown rather than becoming zero.
				CacheRead  string `json:"input_cache_read"`
				CacheWrite string `json:"input_cache_write"`
			} `json:"pricing"`
			// ContextLength is OpenRouter's spelling. Providers that do not
			// publish it omit the field, and the row stays unknown (#81).
			//
			// RawMessage rather than json.Number: a provider that sends a
			// value we cannot read must cost that one row its context window,
			// not the whole listing. Decoding straight into json.Number makes
			// one malformed field fail every unmarshal below it.
			ContextLength json.RawMessage `json:"context_length"`
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
		row := catalog.Model{
			ID:         m.ID,
			Provider:   a.provider,
			AccountID:  a.id,
			Tier:       inferTier(m.ID, a.tier),
			Modalities: catalog.ModalitiesFromLive(m.ID, input, output),
			Status:     "ready",
			Exposed:    true,
			Routable:   true,
		}
		if a.provider == "openrouter" {
			row.Price = openRouterPrice(m.Pricing)
		}
		// Ungated, unlike pricing: context_length is not OpenRouter-specific,
		// and a provider publishing it is opting in by doing so. Gating on the
		// provider name would hide it from every self-hosted gateway that
		// reports it, which is exactly where a user most wants to see it (#81).
		row.ContextWindow = contextWindow(m.ContextLength)
		out = append(out, row)
	}
	return out, nil
}

// listWorkersAIModels uses Cloudflare's catalog. GET {base}/models is
// /ai/v1/models, which Workers AI answers 405. The live list is
// GET /accounts/{id}/ai/models/search. Model ids are the name field
// (@cf/…), which the OpenAI-compat chat route accepts.
func (a *Adapter) listWorkersAIModels(ctx context.Context) ([]catalog.Model, error) {
	var out []catalog.Model
	seen := map[string]struct{}{}
	for page := 1; page <= 20; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, workersSearchURL(a.baseURL, page), nil)
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
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, adapter.NewHTTPError(resp, truncate(body))
		}
		var pageBody struct {
			Success bool `json:"success"`
			Result  []struct {
				Name string          `json:"name"`
				ID   string          `json:"id"`
				Task json.RawMessage `json:"task"`
			} `json:"result"`
			ResultInfo struct {
				Count      int `json:"count"`
				TotalCount int `json:"total_count"`
			} `json:"result_info"`
		}
		if err := json.Unmarshal(body, &pageBody); err != nil {
			return nil, err
		}
		if !pageBody.Success && len(pageBody.Result) == 0 {
			return nil, fmt.Errorf("%s list models: %s", a.provider, truncate(body))
		}
		if len(pageBody.Result) == 0 {
			break
		}
		for _, m := range pageBody.Result {
			id := m.Name
			if id == "" {
				id = m.ID
			}
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, catalog.Model{
				ID:         id,
				Provider:   a.provider,
				AccountID:  a.id,
				Tier:       a.tier,
				Modalities: catalog.ModalitiesFromLive(id, nil, workersOutput(m.Task)),
				Status:     "ready",
				Exposed:    true,
				Routable:   true,
			})
		}
		if pageBody.ResultInfo.TotalCount > 0 && len(out) >= pageBody.ResultInfo.TotalCount {
			break
		}
		if pageBody.ResultInfo.Count > 0 && len(pageBody.Result) < pageBody.ResultInfo.Count {
			break
		}
		if len(pageBody.Result) < 100 {
			break
		}
	}
	return out, nil
}

func workersSearchURL(base string, page int) string {
	base = strings.TrimRight(base, "/")
	root := base
	if strings.HasSuffix(base, "/ai/v1") {
		root = strings.TrimSuffix(base, "/v1")
	}
	return fmt.Sprintf("%s/models/search?page=%d&per_page=100", root, page)
}

func workersOutput(task json.RawMessage) []string {
	name := workersTaskName(task)
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "embedding"):
		return []string{"embeddings"}
	case strings.Contains(lower, "image"):
		return []string{"image"}
	default:
		return []string{"text"}
	}
}

func workersTaskName(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		return s
	}
	var obj struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	return obj.Name
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
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return adapter.ChatResponse{}, adapter.NewHTTPError(resp, truncate(body))
	}
	return adapter.ChatResponse{Model: req.Model, Raw: body, Content: extractContent(body)}, nil
}

// Complete posts a prepared chat body. A JSON object is returned as-is. An SSE
// body is assembled into one chat completion so a non-stream caller still gets text.
func (a *Adapter) Complete(ctx context.Context, model string, raw []byte) (adapter.ChatResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if bytes.Contains(raw, []byte(`"stream":true`)) {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	a.auth(httpReq)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return adapter.ChatResponse{}, adapter.NewHTTPError(resp, truncate(body))
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] == '{' {
		return adapter.ChatResponse{Model: model, Raw: trimmed, Content: extractContent(trimmed)}, nil
	}
	content, assembled, err := assembleOpenAIStream(trimmed)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	return adapter.ChatResponse{Model: model, Raw: assembled, Content: content}, nil
}

func assembleOpenAIStream(body []byte) (string, []byte, error) {
	var text strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil || len(chunk.Choices) == 0 {
			continue
		}
		if chunk.Choices[0].Delta.Content != "" {
			text.WriteString(chunk.Choices[0].Delta.Content)
		} else if chunk.Choices[0].Message.Content != "" && text.Len() == 0 {
			text.WriteString(chunk.Choices[0].Message.Content)
		}
	}
	if err := sc.Err(); err != nil {
		return "", nil, err
	}
	content := text.String()
	assembled, err := json.Marshal(map[string]any{
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]string{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
	})
	return content, assembled, err
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return adapter.NewHTTPError(resp, truncate(body))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (a *Adapter) GenerateImage(ctx context.Context, req adapter.ImageRequest) (adapter.ImageResponse, error) {
	raw := req.Raw
	if len(raw) == 0 {
		payload := struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}{Model: req.Model, Prompt: req.Prompt}
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			return adapter.ImageResponse{}, err
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/images/generations", bytes.NewReader(raw))
	if err != nil {
		return adapter.ImageResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	a.auth(httpReq)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return adapter.ImageResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return adapter.ImageResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return adapter.ImageResponse{}, adapter.NewHTTPError(resp, truncate(body))
	}
	return adapter.ParseImageResponse(body, req.Model), nil
}

func (a *Adapter) EditImage(ctx context.Context, req adapter.ImageRequest) (adapter.ImageResponse, error) {
	raw := req.Raw
	if len(raw) == 0 {
		return adapter.ImageResponse{}, adapter.ErrImageModelRequired
	}
	ct := req.ContentType
	if ct == "" {
		ct = "application/json"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/images/edits", bytes.NewReader(raw))
	if err != nil {
		return adapter.ImageResponse{}, err
	}
	httpReq.Header.Set("Content-Type", ct)
	a.auth(httpReq)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return adapter.ImageResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return adapter.ImageResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return adapter.ImageResponse{}, adapter.NewHTTPError(resp, truncate(body))
	}
	return adapter.ParseImageResponse(body, req.Model), nil
}

func (a *Adapter) CreateEmbeddings(ctx context.Context, req adapter.EmbeddingRequest) (adapter.EmbeddingResponse, error) {
	raw := req.Raw
	if len(raw) == 0 {
		payload := struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}{Model: req.Model, Input: req.Input}
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			return adapter.EmbeddingResponse{}, err
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/embeddings", bytes.NewReader(raw))
	if err != nil {
		return adapter.EmbeddingResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	a.auth(httpReq)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return adapter.EmbeddingResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return adapter.EmbeddingResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return adapter.EmbeddingResponse{}, adapter.NewHTTPError(resp, truncate(body))
	}
	return adapter.ParseEmbeddingResponse(body, req.Model), nil
}

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.ImageGenerator = (*Adapter)(nil)
var _ adapter.Embedder = (*Adapter)(nil)

func (a *Adapter) body(req adapter.ChatRequest, stream bool) ([]byte, error) {
	if len(req.Raw) > 0 {
		return jsonx.SetStream(translate.StripReasoningOpaque(req.Raw), stream), nil
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

func openRouterPrice(pricing *struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
	CacheRead  string `json:"input_cache_read"`
	CacheWrite string `json:"input_cache_write"`
}) catalog.Price {
	if pricing == nil {
		return catalog.Price{}
	}
	input, inOK := parsePerToken(pricing.Prompt)
	output, outOK := parsePerToken(pricing.Completion)
	if !inOK || !outOK {
		return catalog.Price{}
	}
	p := catalog.Price{
		Input: &input, Output: &output,
		Currency: "USD", Source: "openrouter", ObservedAt: time.Now(), Verified: true,
	}
	// An absent or unparseable cache rate stays nil: unknown, not free.
	if v, ok := parsePerToken(pricing.CacheRead); ok {
		p.CacheRead = &v
	}
	if v, ok := parsePerToken(pricing.CacheWrite); ok {
		p.CacheWrite = &v
	}
	return p
}

func parsePerToken(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value < 0 {
		return 0, false
	}
	perMillion := value * 1_000_000
	return perMillion, true
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

// contextWindow reads a published context length. Anything that is not a
// positive integer means the provider did not publish one, which stays zero
// (unknown) rather than becoming a number a harness would trust. A string or a
// float here is a provider doing something unexpected, not a value to guess at.
func contextWindow(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	// A quoted integer is not a guess, it is the same exact number, and
	// providers that serialise it that way are common enough to be worth
	// reading. Anything else -- a float, a word, an object -- is unknown.
	s := string(raw)
	if unquoted, ok := unquoteJSONString(s); ok {
		s = unquoted
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil || i <= 0 || i > math.MaxInt32 {
		return 0
	}
	return int(i)
}

// unquoteJSONString returns the contents of a JSON string literal, and false
// for anything else.
func unquoteJSONString(s string) (string, bool) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", false
	}
	var out string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return "", false
	}
	return out, true
}
