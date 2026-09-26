package adapters

import (
	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/anthropic_oauth"
	"github.com/ks1686/peaproxy/internal/adapter/ollama"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/adapter/openai_oauth"
)

// DefaultRegistry registers built-in adapter factories for the server and CLI.
func DefaultRegistry() *adapter.Registry {
	r := adapter.NewRegistry()
	r.Register(ollama.Name, ollama.New)
	r.Register(openai_compat.Name, openai_compat.New)
	r.Register(anthropic_oauth.Name, anthropic_oauth.New)
	r.Register(openai_oauth.Name, openai_oauth.New)
	return r
}
