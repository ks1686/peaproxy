package adapters

import (
	"sort"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/anthropic"
	"github.com/ks1686/peaproxy/internal/adapter/anthropic_oauth"
	"github.com/ks1686/peaproxy/internal/adapter/antigravity"
	"github.com/ks1686/peaproxy/internal/adapter/copilot_oauth"
	"github.com/ks1686/peaproxy/internal/adapter/cursoragent"
	"github.com/ks1686/peaproxy/internal/adapter/factory_oauth"
	"github.com/ks1686/peaproxy/internal/adapter/hosted"
	"github.com/ks1686/peaproxy/internal/adapter/kimi_oauth"
	"github.com/ks1686/peaproxy/internal/adapter/meta_oauth"
	"github.com/ks1686/peaproxy/internal/adapter/ollama"
	"github.com/ks1686/peaproxy/internal/adapter/openai"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/adapter/openai_oauth"
	"github.com/ks1686/peaproxy/internal/adapter/opencodego"
	"github.com/ks1686/peaproxy/internal/adapter/opencodezen"
	"github.com/ks1686/peaproxy/internal/adapter/openrouter"
	"github.com/ks1686/peaproxy/internal/adapter/qwen_oauth"
	"github.com/ks1686/peaproxy/internal/adapter/xai_oauth"
)

// DefaultRegistry registers built-in adapter factories for the server and CLI.
func DefaultRegistry() *adapter.Registry {
	r := adapter.NewRegistry()
	r.Register(ollama.Name, ollama.New)
	r.Register(openai_compat.Name, openai_compat.New)
	r.Register(openai.Name, openai.New)
	r.Register(anthropic.Name, anthropic.New)
	r.Register(openrouter.Name, openrouter.New)
	r.Register(cursoragent.Name, cursoragent.New)
	r.Register(opencodezen.Name, opencodezen.New)
	r.Register(opencodego.Name, opencodego.New)
	for _, spec := range hosted.All() {
		r.Register(spec.Name, hosted.Wrap(spec))
	}
	// Alias: Gemini is Google AI Studio's official OpenAI-compat endpoint.
	r.Register("gemini", hosted.Wrap(hosted.Spec{
		Name:           "gemini",
		DefaultBaseURL: hosted.Google.DefaultBaseURL,
		DefaultTier:    hosted.Google.DefaultTier,
		EnvKey:         hosted.Google.EnvKey,
		Notes:          hosted.Google.Notes,
	}))
	r.Register(anthropic_oauth.Name, anthropic_oauth.New)
	r.Register(openai_oauth.Name, openai_oauth.New)
	r.Register(antigravity.Name, antigravity.New)
	r.Register(antigravity.AliasGemini, antigravity.New)
	r.Register(xai_oauth.Name, xai_oauth.New)
	r.Register(kimi_oauth.Name, kimi_oauth.New)
	r.Register(kimi_oauth.NameAI, kimi_oauth.NewAI)
	r.Register(meta_oauth.Name, meta_oauth.New)
	r.Register(qwen_oauth.Name, qwen_oauth.New)
	r.Register(copilot_oauth.Name, copilot_oauth.New)
	r.Register(factory_oauth.Name, factory_oauth.New)
	return r
}

// Names returns sorted factory names from DefaultRegistry.
func Names() []string {
	n := DefaultRegistry().Names()
	sort.Strings(n)
	return n
}
