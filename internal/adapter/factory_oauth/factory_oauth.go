// Package factory_oauth is a stub: Factory has no public consumer chat OAuth.
//
// Droid is a coding harness (client of PeaProxy), not a chat-model upstream.
// Official Factory HTTP is sessions/CI/computers at api.factory.ai, not
// OpenAI-compat chat. See docs/OAUTH.md and docs/HARNESS.md.
package factory_oauth

import (
	"context"
	"fmt"
	"io"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

const Name = "factory_oauth"

const notYet = "factory_oauth: not yet — Factory has no public consumer chat OAuth or OpenAI-compat chat API; use Droid as a PeaProxy client (`peaproxy clients show droid`) or a Factory API key only for the documented sessions/CI API at https://api.factory.ai"

// Adapter advertises OAuth so the CLI can return a clear not-yet error.
type Adapter struct {
	id string
}

// New returns the stub adapter.
func New(opts adapter.Options) (adapter.Adapter, error) {
	id := opts.ID
	if id == "" {
		id = Name
	}
	return &Adapter{id: id}, nil
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{OAuth: true, NeedsAuth: true, ListModels: false, Chat: false}
}

func (a *Adapter) AuthStart(ctx context.Context) (adapter.AuthSession, error) {
	_ = ctx
	return adapter.AuthSession{}, fmt.Errorf("%s", notYet)
}

func (a *Adapter) AuthComplete(ctx context.Context, session adapter.AuthSession, code string) error {
	_ = ctx
	_ = session
	_ = code
	return fmt.Errorf("%s", notYet)
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

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.Authenticator = (*Adapter)(nil)
