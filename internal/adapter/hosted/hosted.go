// Package hosted registers thin OpenAI-compat wrappers with known base URLs and tiers.
package hosted

import (
	"os"
	"strings"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/catalog"
)

// Spec is one named hosted/local OpenAI-compat endpoint.
type Spec struct {
	Name           string
	DefaultBaseURL string
	DefaultTier    catalog.Tier
	EnvKey         string
	AccountIDEnv   string
	URLPlaceholder string
	Notes          string
}

// Known first-class presets. Google AI Studio uses the official OpenAI-compat
// Gemini endpoint (https://ai.google.dev/gemini-api/docs/openai) — not generateContent.
var (
	LMStudio = Spec{
		Name:           "lmstudio",
		DefaultBaseURL: "http://127.0.0.1:1234/v1",
		DefaultTier:    catalog.TierLocal,
		Notes:          "Local LM Studio server; no key required.",
	}
	Groq = Spec{
		Name:           "groq",
		DefaultBaseURL: "https://api.groq.com/openai/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "GROQ_API_KEY",
		Notes:          "Groq OpenAI-compat (https://api.groq.com/openai/v1). Key env: GROQ_API_KEY.",
	}
	Cerebras = Spec{
		Name:           "cerebras",
		DefaultBaseURL: "https://api.cerebras.ai/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "CEREBRAS_API_KEY",
		Notes:          "Cerebras OpenAI-compat (https://api.cerebras.ai/v1). Key env: CEREBRAS_API_KEY.",
	}
	Google = Spec{
		Name:           "google",
		DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "GEMINI_API_KEY",
		Notes:          "Google AI Studio via official OpenAI-compat Gemini API. Native generateContent is not used.",
	}
	XAI = Spec{
		Name:           "xai",
		DefaultBaseURL: "https://api.x.ai/v1",
		DefaultTier:    catalog.TierPaid,
		EnvKey:         "XAI_API_KEY",
		Notes:          "Official xAI OpenAI-compat (https://api.x.ai/v1). Key env: XAI_API_KEY. Safer than subscription OAuth.",
	}
	HuggingFace = Spec{
		Name:           "huggingface",
		DefaultBaseURL: "https://router.huggingface.co/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "HF_TOKEN",
		Notes:          "Hugging Face Inference Providers OpenAI-compat router. Token env: HF_TOKEN.",
	}
	LlamaCpp = Spec{
		Name:           "llamacpp",
		DefaultBaseURL: "http://127.0.0.1:8080/v1",
		DefaultTier:    catalog.TierLocal,
		Notes:          "llama.cpp llama-server OpenAI-compat (default :8080). Override baseURL if you changed --port.",
	}
	VLLM = Spec{
		Name:           "vllm",
		DefaultBaseURL: "http://127.0.0.1:8000/v1",
		DefaultTier:    catalog.TierLocal,
		Notes:          "vLLM OpenAI-compat server (default :8000). Override baseURL if you changed --port.",
	}
	Jan = Spec{
		Name:           "jan",
		DefaultBaseURL: "http://127.0.0.1:1337/v1",
		DefaultTier:    catalog.TierLocal,
		Notes:          "Jan desktop Local API Server (default :1337). Start it in Settings > Local API Server; optional Bearer if you set a key there.",
	}
	GPT4All = Spec{
		Name:           "gpt4all",
		DefaultBaseURL: "http://127.0.0.1:4891/v1",
		DefaultTier:    catalog.TierLocal,
		Notes:          "GPT4All desktop local API (default :4891). Enable in Settings > Application > Advanced. No key required.",
	}
	NIM = Spec{
		Name:           "nim",
		DefaultBaseURL: "https://integrate.api.nvidia.com/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "NVIDIA_API_KEY",
		Notes:          "NVIDIA API catalog / hosted NIM. Free trial key from build.nvidia.com. Local NIM containers should use the vLLM or custom OpenAI-compat preset instead.",
	}
	WorkersAI = Spec{
		Name:           "workers_ai",
		DefaultBaseURL: "https://api.cloudflare.com/client/v4/accounts/YOUR_ACCOUNT_ID/ai/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "CLOUDFLARE_API_TOKEN",
		AccountIDEnv:   "CLOUDFLARE_ACCOUNT_ID",
		URLPlaceholder: "YOUR_ACCOUNT_ID",
		Notes:          "Cloudflare Workers AI OpenAI-compat (accounts/<id>/ai/v1). Paste the account id (wrangler whoami or the dashboard) or set CLOUDFLARE_ACCOUNT_ID. Token env: CLOUDFLARE_API_TOKEN. Never leave YOUR_ACCOUNT_ID in the URL.",
	}
	OllamaCloud = Spec{
		Name:           "ollama_cloud",
		DefaultBaseURL: "https://ollama.com/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "OLLAMA_API_KEY",
		Notes:          "Ollama Cloud hosted OpenAI-compat (not local :11434). Key from ollama.com/settings/keys. Env: OLLAMA_API_KEY.",
	}
	SambaNova = Spec{
		Name:           "sambanova",
		DefaultBaseURL: "https://api.sambanova.ai/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "SAMBANOVA_API_KEY",
		Notes:          "SambaCloud OpenAI-compat (https://api.sambanova.ai/v1). Key env: SAMBANOVA_API_KEY. Free-tier limits apply.",
	}
)

// All returns the hosted specs in UI order.
func All() []Spec {
	return []Spec{LMStudio, LlamaCpp, VLLM, Jan, GPT4All, Groq, Cerebras, Google, XAI, HuggingFace, NIM, WorkersAI, OllamaCloud, SambaNova}
}

// Lookup returns a named hosted spec.
func Lookup(name string) (Spec, bool) {
	for _, spec := range All() {
		if spec.Name == name {
			return spec, true
		}
	}
	return Spec{}, false
}

// FillBaseURL applies the default URL and, when the URL still contains the
// account-id placeholder, substitutes os.Getenv(AccountIDEnv). The env value
// is never returned on its own.
func (s Spec) FillBaseURL(baseURL string) string {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = s.DefaultBaseURL
	}
	if s.URLPlaceholder == "" || s.AccountIDEnv == "" {
		return baseURL
	}
	if !strings.Contains(baseURL, s.URLPlaceholder) {
		return baseURL
	}
	id := strings.TrimSpace(os.Getenv(s.AccountIDEnv))
	if id == "" {
		return baseURL
	}
	return strings.ReplaceAll(baseURL, s.URLPlaceholder, id)
}

// EnvPresent reports whether a named env var is non-empty without returning its value.
func EnvPresent(name string) bool {
	return name != "" && strings.TrimSpace(os.Getenv(name)) != ""
}

// Wrap returns a factory that fills default base URL and tier then delegates to openai_compat.
func Wrap(spec Spec) adapter.Factory {
	return func(opts adapter.Options) (adapter.Adapter, error) {
		opts.BaseURL = spec.FillBaseURL(opts.BaseURL)
		if opts.Tier == "" {
			opts.Tier = spec.DefaultTier
		}
		inner, err := openai_compat.New(opts)
		if err != nil {
			return nil, err
		}
		if named, ok := inner.(*openai_compat.Adapter); ok {
			named.SetProviderName(spec.Name)
		}
		return inner, nil
	}
}
