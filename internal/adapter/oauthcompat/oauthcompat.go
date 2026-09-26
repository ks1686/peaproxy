// Package oauthcompat wraps openai_compat for subscription-OAuth accounts.
package oauthcompat

import (
	"context"
	"io"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/catalog"
)

// Tagged marks live OpenAI-compat models as subscription OAuth.
type Tagged struct {
	Inner    adapter.Adapter
	Provider string
	Account  string
}

// Open builds an OpenAI-compat client and tags catalog rows.
func Open(opts adapter.Options, provider string) (*Tagged, error) {
	inner, err := openai_compat.New(opts)
	if err != nil {
		return nil, err
	}
	if named, ok := inner.(*openai_compat.Adapter); ok {
		named.SetProviderName(provider)
	}
	acct := opts.ID
	if acct == "" {
		acct = provider
	}
	return &Tagged{Inner: inner, Provider: provider, Account: acct}, nil
}

func (t *Tagged) ID() string { return t.Inner.ID() }

func (t *Tagged) Capabilities() adapter.Capabilities {
	c := t.Inner.Capabilities()
	c.OAuth = true
	c.APIKey = false
	c.ImageOut = false // subscription OAuth does not proxy /images/generations
	return c
}

func (t *Tagged) Validate(ctx context.Context) error {
	return t.Inner.Validate(ctx)
}

func (t *Tagged) ListModels(ctx context.Context) ([]catalog.Model, error) {
	models, err := t.Inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range models {
		models[i].SubscriptionOAuth = true
		models[i].Provider = t.Provider
		models[i].AccountID = t.Account
		models[i].Tier = catalog.TierPaid
		models[i].Exposed = true
		models[i].Routable = true
	}
	return models, nil
}

func (t *Tagged) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	return t.Inner.Chat(ctx, req)
}

func (t *Tagged) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	return t.Inner.ChatStream(ctx, req, w)
}

var _ adapter.Adapter = (*Tagged)(nil)
