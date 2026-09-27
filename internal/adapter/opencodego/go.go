// Package opencodego is a named OpenCode Go subscription adapter.
//
// Official path is an API key from https://opencode.ai/auth (no public OAuth).
// Distinct from OpenCode Zen (`opencode_zen` at /zen/v1). See docs/PROVIDERS.md.
package opencodego

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/catalog"
)

const (
	Name           = "opencode_go"
	DefaultBaseURL = "https://opencode.ai/zen/go/v1"
	UserAgent      = "peaproxy"
	notOAuth       = "opencode_go: OpenCode Go uses an API key from https://opencode.ai/auth (no public OAuth). Add the OpenCode Go Accounts preset or: peaproxy accounts add opencode-go"
)

// Adapter wraps openai_compat against OpenCode Go and tags free/privacy models.
type Adapter struct {
	inner adapter.Adapter
	id    string
}

// New points at Go. Subscribe at opencode.ai, then paste the Go API key.
func New(opts adapter.Options) (adapter.Adapter, error) {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.Tier == "" {
		opts.Tier = catalog.TierPaid
	}
	session := strings.TrimSpace(opts.SessionID)
	if session == "" {
		session = opts.ID
	}
	if session == "" {
		session = Name
	}
	if opts.ExtraHeaders == nil {
		opts.ExtraHeaders = map[string]string{}
	}
	if opts.ExtraHeaders["User-Agent"] == "" {
		opts.ExtraHeaders["User-Agent"] = UserAgent
	}
	if opts.ExtraHeaders["X-Opencode-Session"] == "" {
		opts.ExtraHeaders["X-Opencode-Session"] = session
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
	c.OAuth = false
	return c
}

func (a *Adapter) AuthStart(ctx context.Context) (adapter.AuthSession, error) {
	_ = ctx
	return adapter.AuthSession{}, fmt.Errorf("%s", notOAuth)
}

func (a *Adapter) AuthComplete(ctx context.Context, session adapter.AuthSession, code string) error {
	_ = ctx
	_ = session
	_ = code
	return fmt.Errorf("%s", notOAuth)
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
		models[i].Tier = catalog.TierPaid
		if looksFree(models[i].ID) || looksFree(models[i].DisplayName) {
			models[i].Tier = catalog.TierFree
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

func (a *Adapter) EditImage(ctx context.Context, req adapter.ImageRequest) (adapter.ImageResponse, error) {
	return adapter.EditImageFrom(a.inner, ctx, req)
}

func (a *Adapter) CreateEmbeddings(ctx context.Context, req adapter.EmbeddingRequest) (adapter.EmbeddingResponse, error) {
	return adapter.EmbedFrom(a.inner, ctx, req)
}

func looksFree(id string) bool {
	lower := strings.ToLower(id)
	if strings.HasSuffix(lower, "-free") || strings.Contains(lower, "-free-") || strings.Contains(lower, " free") {
		return true
	}
	return strings.Contains(lower, "space bunny") || strings.Contains(lower, "space-bunny")
}

func privacyNote(id string) string {
	lower := strings.ToLower(id)
	if strings.Contains(lower, "muse") {
		return "OpenCode Go Muse Spark contributor models may use prompts for training. Review https://opencode.ai/docs/go/ before sending private code."
	}
	return ""
}

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.Authenticator = (*Adapter)(nil)
