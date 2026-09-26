package openai

import (
	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/catalog"
)

const (
	Name           = "openai"
	DefaultBaseURL = "https://api.openai.com/v1"
)

// New is a first-class OpenAI API-key adapter (OpenAI-compat wire + default base URL).
func New(opts adapter.Options) (adapter.Adapter, error) {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.Tier == "" {
		opts.Tier = catalog.TierPaid
	}
	inner, err := openai_compat.New(opts)
	if err != nil {
		return nil, err
	}
	if named, ok := inner.(*openai_compat.Adapter); ok {
		named.SetProviderName(Name)
	}
	return inner, nil
}
