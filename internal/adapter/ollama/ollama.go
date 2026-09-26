package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/catalog"
)

const (
	Name           = "ollama"
	DefaultBaseURL = "http://127.0.0.1:11434/v1"
)

// Adapter talks to a local Ollama OpenAI-compatible server, with /api/tags fallback.
type Adapter struct {
	inner     adapter.Adapter
	id        string
	compatURL string
	nativeURL string
	client    *http.Client
}

// New constructs an Ollama adapter.
func New(opts adapter.Options) (adapter.Adapter, error) {
	base := opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	opts.BaseURL = base
	opts.Tier = catalog.TierLocal
	inner, err := openai_compat.New(opts)
	if err != nil {
		return nil, err
	}
	if named, ok := inner.(*openai_compat.Adapter); ok {
		named.SetProviderName(Name)
	}
	id := opts.ID
	if id == "" {
		id = Name
	}
	compat := strings.TrimRight(base, "/")
	native := strings.TrimSuffix(compat, "/v1")
	return &Adapter{
		inner:     inner,
		id:        id,
		compatURL: compat,
		nativeURL: native,
		client:    &http.Client{Timeout: 3 * time.Second},
	}, nil
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	c := a.inner.Capabilities()
	c.Local = true
	return c
}

func (a *Adapter) Validate(ctx context.Context) error {
	_, err := a.ListModels(ctx)
	return err
}

func (a *Adapter) ListModels(ctx context.Context) ([]catalog.Model, error) {
	models, err := a.inner.ListModels(ctx)
	if err == nil && len(models) > 0 {
		return tagLocal(models, a.id), nil
	}
	tags, tagsErr := a.listTags(ctx)
	if tagsErr == nil {
		return tags, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, tagsErr
}

func (a *Adapter) listTags(ctx context.Context) ([]catalog.Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.nativeURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama /api/tags: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, adapter.HTTPError{Status: resp.StatusCode, Body: string(body)}
	}
	var parsed struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]catalog.Model, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		out = append(out, catalog.Model{
			ID:         m.Name,
			Provider:   Name,
			AccountID:  a.id,
			Tier:       catalog.TierLocal,
			Modalities: catalog.InferModalities(m.Name),
			Status:     "ready",
			Exposed:    true,
			Routable:   true,
		})
	}
	return out, nil
}

func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	return a.inner.Chat(ctx, req)
}

func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	return a.inner.ChatStream(ctx, req, w)
}

func (a *Adapter) GenerateImage(ctx context.Context, req adapter.ImageRequest) (adapter.ImageResponse, error) {
	return adapter.GenerateImageFrom(a.inner, ctx, req)
}

func (a *Adapter) CreateEmbeddings(ctx context.Context, req adapter.EmbeddingRequest) (adapter.EmbeddingResponse, error) {
	return adapter.EmbedFrom(a.inner, ctx, req)
}

func tagLocal(models []catalog.Model, account string) []catalog.Model {
	for i := range models {
		models[i].Provider = Name
		models[i].AccountID = account
		models[i].Tier = catalog.TierLocal
	}
	return models
}
