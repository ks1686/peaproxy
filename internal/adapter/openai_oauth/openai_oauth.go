// Package openai_oauth is a placeholder for ChatGPT/Codex subscription OAuth.
//
// TODO(spike): implement official/user-consented OAuth only. Do not reverse-engineer
// private login flows or ship harvested client secrets.
package openai_oauth

import (
	"context"
	"io"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

const Name = "openai_oauth"

// Adapter is a non-functional OAuth stub. Auth and Chat return ErrNotImplemented.
type Adapter struct {
	id string
}

// New returns the stub. Spike work lives in a later PR.
func New(opts adapter.Options) (adapter.Adapter, error) {
	id := opts.ID
	if id == "" {
		id = Name
	}
	return &Adapter{id: id}, nil
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		Chat:      true,
		Stream:    true,
		VisionIn:  true,
		OAuth:     true,
		NeedsAuth: true,
	}
}

func (a *Adapter) ListModels(ctx context.Context) ([]catalog.Model, error) {
	_ = ctx
	return nil, adapter.ErrNotImplemented
}

func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	_ = ctx
	_ = req
	return adapter.ChatResponse{}, adapter.ErrNotImplemented
}

func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	_ = ctx
	_ = req
	_ = w
	return adapter.ErrNotImplemented
}

func (a *Adapter) Validate(ctx context.Context) error {
	_ = ctx
	return adapter.ErrNotImplemented
}

func (a *Adapter) AuthStart(ctx context.Context) (adapter.AuthSession, error) {
	_ = ctx
	return adapter.AuthSession{Provider: Name}, adapter.ErrNotImplemented
}

func (a *Adapter) AuthComplete(ctx context.Context, session adapter.AuthSession, code string) error {
	_ = ctx
	_ = session
	_ = code
	return adapter.ErrNotImplemented
}

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.Authenticator = (*Adapter)(nil)
