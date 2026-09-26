package openrouter

import (
	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/catalog"
)

const (
	Name           = "openrouter"
	DefaultBaseURL = "https://openrouter.ai/api/v1"
)

// New is the OpenRouter preset. IDs ending in :free are tagged free; others keep fallback tier.
func New(opts adapter.Options) (adapter.Adapter, error) {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.Tier == "" {
		opts.Tier = catalog.TierFreemium
	}
	if opts.ExtraHeaders == nil {
		opts.ExtraHeaders = map[string]string{}
	}
	if opts.ExtraHeaders["HTTP-Referer"] == "" {
		opts.ExtraHeaders["HTTP-Referer"] = "https://github.com/ks1686/peaproxy"
	}
	if opts.ExtraHeaders["X-Title"] == "" {
		opts.ExtraHeaders["X-Title"] = "PeaProxy"
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
