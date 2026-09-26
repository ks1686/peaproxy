// Package opencodezen is a named OpenCode Zen adapter (CPA declined #6018).
package opencodezen

import (
	"context"
	"io"
	"strings"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/catalog"
)

const (
	Name           = "opencode_zen"
	DefaultBaseURL = "https://opencode.ai/zen/v1"
)

// Adapter wraps openai_compat against OpenCode Zen and tags free/privacy models.
type Adapter struct {
	inner adapter.Adapter
	id    string
}

// New points at Zen. Prefer an API key from opencode.ai. Empty Bearer + x-session-id
// is a community path and may violate ToS — we still send sessionId if configured.
func New(opts adapter.Options) (adapter.Adapter, error) {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.Tier == "" {
		opts.Tier = catalog.TierFree
	}
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
	return &Adapter{inner: inner, id: id}, nil
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	c := a.inner.Capabilities()
	c.APIKey = true
	return c
}

func (a *Adapter) Validate(ctx context.Context) error { return a.inner.Validate(ctx) }

func (a *Adapter) ListModels(ctx context.Context) ([]catalog.Model, error) {
	models, err := a.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range models {
		models[i].Provider = Name
		models[i].AccountID = a.id
		models[i].Tier = catalog.TierFree
		if !looksFree(models[i].ID) && !looksFree(models[i].DisplayName) {
			models[i].Tier = catalog.TierPaid
		}
		models[i].PrivacyNote = privacyNote(models[i].ID)
	}
	return models, nil
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

func looksFree(id string) bool {
	lower := strings.ToLower(id)
	if strings.HasSuffix(lower, "-free") || strings.Contains(lower, "-free-") || strings.Contains(lower, " free") {
		return true
	}
	for _, n := range []string{"big pickle", "space bunny", "jev 1.13"} {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

func privacyNote(id string) string {
	lower := strings.ToLower(id)
	for _, n := range []string{"nemotron", "big pickle", "big-pickle", "mimo", "muse"} {
		if strings.Contains(lower, n) {
			return "OpenCode Zen free model may use prompts for training. Review https://opencode.ai/docs/zen/ before sending private code."
		}
	}
	return ""
}
