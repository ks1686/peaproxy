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

	// Unverified marks a preset PeaProxy has not yet called against the live
	// API. The base URL comes from the provider's documentation, but a
	// documented endpoint is not a tested one, and the UI says which is which.
	Unverified bool

	// Group is the accounts-dropdown section for presets built from this spec.
	// Empty means the caller decides, which keeps a spec usable outside the UI.
	// The values mirror the group names in the adapters package; that package
	// depends on this one, so the constants cannot be shared without a cycle.
	Group string
}

// Accounts-dropdown sections. These strings are part of the admin API payload.
const (
	GroupLocal  = "Local models"
	GroupAPIKey = "Official API keys"
	GroupOther  = "Other"
	GroupOAuth  = "Subscription OAuth"
)

// Known first-class presets. Google AI Studio uses the official OpenAI-compat
// Gemini endpoint (https://ai.google.dev/gemini-api/docs/openai) — not generateContent.
var (
	LMStudio = Spec{
		Name:           "lmstudio",
		DefaultBaseURL: "http://127.0.0.1:1234/v1",
		DefaultTier:    catalog.TierLocal,
		Notes:          "Local LM Studio server; no key required.",
		Group:          GroupLocal,
	}
	Groq = Spec{
		Name:           "groq",
		DefaultBaseURL: "https://api.groq.com/openai/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "GROQ_API_KEY",
		Notes:          "Groq OpenAI-compat (https://api.groq.com/openai/v1). Key env: GROQ_API_KEY.",
		Group:          GroupAPIKey,
	}
	Cerebras = Spec{
		Name:           "cerebras",
		DefaultBaseURL: "https://api.cerebras.ai/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "CEREBRAS_API_KEY",
		Notes:          "Cerebras OpenAI-compat (https://api.cerebras.ai/v1). Key env: CEREBRAS_API_KEY.",
		Group:          GroupAPIKey,
	}
	Google = Spec{
		Name:           "google",
		DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "GEMINI_API_KEY",
		Notes:          "Google AI Studio via official OpenAI-compat Gemini API. Native generateContent is not used.",
		Group:          GroupAPIKey,
	}
	XAI = Spec{
		Name:           "xai",
		DefaultBaseURL: "https://api.x.ai/v1",
		DefaultTier:    catalog.TierPaid,
		EnvKey:         "XAI_API_KEY",
		Notes:          "Official xAI OpenAI-compat (https://api.x.ai/v1). Key env: XAI_API_KEY. Safer than subscription OAuth.",
		Group:          GroupAPIKey,
	}
	HuggingFace = Spec{
		Name:           "huggingface",
		DefaultBaseURL: "https://router.huggingface.co/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "HF_TOKEN",
		Notes:          "Hugging Face Inference Providers OpenAI-compat router. Token env: HF_TOKEN.",
		Group:          GroupAPIKey,
	}
	LlamaCpp = Spec{
		Name:           "llamacpp",
		DefaultBaseURL: "http://127.0.0.1:8080/v1",
		DefaultTier:    catalog.TierLocal,
		Notes:          "llama.cpp llama-server OpenAI-compat (default :8080). Override baseURL if you changed --port.",
		Group:          GroupLocal,
	}
	VLLM = Spec{
		Name:           "vllm",
		DefaultBaseURL: "http://127.0.0.1:8000/v1",
		DefaultTier:    catalog.TierLocal,
		Notes:          "vLLM OpenAI-compat server (default :8000). Override baseURL if you changed --port.",
		Group:          GroupLocal,
	}
	Jan = Spec{
		Name:           "jan",
		DefaultBaseURL: "http://127.0.0.1:1337/v1",
		DefaultTier:    catalog.TierLocal,
		Notes:          "Jan desktop Local API Server (default :1337). Start it in Settings > Local API Server; optional Bearer if you set a key there.",
		Group:          GroupLocal,
	}
	GPT4All = Spec{
		Name:           "gpt4all",
		DefaultBaseURL: "http://127.0.0.1:4891/v1",
		DefaultTier:    catalog.TierLocal,
		Notes:          "GPT4All desktop local API (default :4891). Enable in Settings > Application > Advanced. No key required.",
		Group:          GroupLocal,
	}
	NIM = Spec{
		Name:           "nim",
		DefaultBaseURL: "https://integrate.api.nvidia.com/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "NVIDIA_API_KEY",
		Notes:          "NVIDIA API catalog / hosted NIM. Free trial key from build.nvidia.com. Local NIM containers should use the vLLM or custom OpenAI-compat preset instead.",
		Group:          GroupAPIKey,
	}
	WorkersAI = Spec{
		Name:           "workers_ai",
		DefaultBaseURL: "https://api.cloudflare.com/client/v4/accounts/YOUR_ACCOUNT_ID/ai/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "CLOUDFLARE_API_TOKEN",
		AccountIDEnv:   "CLOUDFLARE_ACCOUNT_ID",
		URLPlaceholder: "YOUR_ACCOUNT_ID",
		Notes:          "Cloudflare Workers AI OpenAI-compat (accounts/<id>/ai/v1). Paste the account id (wrangler whoami or the dashboard) or set CLOUDFLARE_ACCOUNT_ID. Token env: CLOUDFLARE_API_TOKEN. Never leave YOUR_ACCOUNT_ID in the URL.",
		Group:          GroupAPIKey,
	}
	OllamaCloud = Spec{
		Name:           "ollama_cloud",
		DefaultBaseURL: "https://ollama.com/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "OLLAMA_API_KEY",
		Notes:          "Ollama Cloud hosted OpenAI-compat (not local :11434). Key from ollama.com/settings/keys. Env: OLLAMA_API_KEY.",
		Group:          GroupOther,
	}
	SambaNova = Spec{
		Name:           "sambanova",
		DefaultBaseURL: "https://api.sambanova.ai/v1",
		DefaultTier:    catalog.TierFreemium,
		EnvKey:         "SAMBANOVA_API_KEY",
		Notes:          "SambaCloud OpenAI-compat (https://api.sambanova.ai/v1). Key env: SAMBANOVA_API_KEY. Free-tier limits apply.",
		Group:          GroupAPIKey,
	}
)

// Named key providers from issue #114. All speak OpenAI chat completions, so
// each is a thin spec over openai_compat rather than a new adapter. Base URLs
// are from official documentation; none has been exercised live yet, which is
// why every one of them is marked Unverified.
var (
	AlibabaCoding = Spec{
		Name:           "alibabacoding",
		DefaultBaseURL: "https://coding-intl.dashscope.aliyuncs.com/v1",
		DefaultTier:    catalog.TierPaid,
		EnvKey:         "DASHSCOPE_API_KEY",
		Notes:          "Alibaba Cloud Coding Plan. Chat completions only; not the general Model Studio endpoint. Not live-verified by PeaProxy.",
		Unverified:     true,
		Group:          GroupAPIKey,
	}
	DeepSeek = Spec{
		Name:           "deepseek",
		DefaultBaseURL: "https://api.deepseek.com/v1",
		DefaultTier:    catalog.TierPaid,
		EnvKey:         "DEEPSEEK_API_KEY",
		Notes:          "DeepSeek OpenAI-compatible API. Not live-verified by PeaProxy.",
		Unverified:     true,
		Group:          GroupAPIKey,
	}
	Mistral = Spec{
		Name:           "mistral",
		DefaultBaseURL: "https://api.mistral.ai/v1",
		DefaultTier:    catalog.TierPaid,
		EnvKey:         "MISTRAL_API_KEY",
		Notes:          "Mistral La Plateforme. Distinct from the Claude subscription OAuth adapter. Not live-verified by PeaProxy.",
		Unverified:     true,
		Group:          GroupAPIKey,
	}
	ZAI = Spec{
		Name:           "zai",
		DefaultBaseURL: "https://api.z.ai/api/paas/v4",
		DefaultTier:    catalog.TierPaid,
		EnvKey:         "ZAI_API_KEY",
		Notes:          "Z.AI GLM OpenAI-compatible endpoint. Not live-verified by PeaProxy.",
		Unverified:     true,
		Group:          GroupAPIKey,
	}
	MiniMax = Spec{
		Name:           "minimax",
		DefaultBaseURL: "https://api.minimax.io/v1",
		DefaultTier:    catalog.TierPaid,
		EnvKey:         "MINIMAX_API_KEY",
		Notes:          "MiniMax OpenAI-compatible endpoint. Not live-verified by PeaProxy.",
		Unverified:     true,
		Group:          GroupAPIKey,
	}
	Together = Spec{
		Name:           "together",
		DefaultBaseURL: "https://api.together.xyz/v1",
		DefaultTier:    catalog.TierPaid,
		EnvKey:         "TOGETHER_API_KEY",
		Notes:          "Together AI. Pay per token; not a free tier. Not live-verified by PeaProxy.",
		Unverified:     true,
		Group:          GroupAPIKey,
	}
	Fireworks = Spec{
		Name:           "fireworks",
		DefaultBaseURL: "https://api.fireworks.ai/inference/v1",
		DefaultTier:    catalog.TierPaid,
		EnvKey:         "FIREWORKS_API_KEY",
		Notes:          "Fireworks AI inference. Pay per token. Not live-verified by PeaProxy.",
		Unverified:     true,
		Group:          GroupAPIKey,
	}
	Cohere = Spec{
		Name:           "cohere",
		DefaultBaseURL: "https://api.cohere.ai/compatibility/v1",
		DefaultTier:    catalog.TierPaid,
		EnvKey:         "COHERE_API_KEY",
		Notes:          "Cohere OpenAI compatibility layer. Trial keys are rate limited. Not live-verified by PeaProxy.",
		Unverified:     true,
		Group:          GroupAPIKey,
	}
)

// All returns the hosted specs in UI order.
func All() []Spec {
	return []Spec{LMStudio, LlamaCpp, VLLM, Jan, GPT4All, Groq, Cerebras, Google, XAI, HuggingFace, NIM, WorkersAI, OllamaCloud, SambaNova,
		AlibabaCoding, DeepSeek, Mistral, ZAI, MiniMax, Together, Fireworks, Cohere}
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
