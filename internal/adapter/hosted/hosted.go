// Package hosted registers thin OpenAI-compat wrappers with known base URLs and tiers.
package hosted

import (
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
	}
	Cerebras = Spec{
		Name:           "cerebras",
		DefaultBaseURL: "https://api.cerebras.ai/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "CEREBRAS_API_KEY",
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
	}
	HuggingFace = Spec{
		Name:           "huggingface",
		DefaultBaseURL: "https://router.huggingface.co/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "HF_TOKEN",
		Notes:          "Hugging Face Inference Providers OpenAI-compat router.",
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
		Notes:          "Cloudflare Workers AI OpenAI-compat. Replace YOUR_ACCOUNT_ID in the base URL (wrangler whoami). Token: CLOUDFLARE_API_TOKEN.",
	}
	OllamaCloud = Spec{
		Name:           "ollama_cloud",
		DefaultBaseURL: "https://ollama.com/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "OLLAMA_API_KEY",
		Notes:          "Ollama Cloud hosted OpenAI-compat (not local :11434). Key from ollama.com/settings/keys.",
	}
)

// All returns the hosted specs in UI order.
func All() []Spec {
	return []Spec{LMStudio, LlamaCpp, VLLM, Groq, Cerebras, Google, XAI, HuggingFace, NIM, WorkersAI, OllamaCloud}
}

// Wrap returns a factory that fills default base URL and tier then delegates to openai_compat.
func Wrap(spec Spec) adapter.Factory {
	return func(opts adapter.Options) (adapter.Adapter, error) {
		if opts.BaseURL == "" {
			opts.BaseURL = spec.DefaultBaseURL
		}
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
