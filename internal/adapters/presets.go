package adapters

import (
	"github.com/ks1686/peaproxy/internal/adapter/anthropic"
	"github.com/ks1686/peaproxy/internal/adapter/hosted"
	"github.com/ks1686/peaproxy/internal/adapter/ollama"
	"github.com/ks1686/peaproxy/internal/adapter/openai"
	"github.com/ks1686/peaproxy/internal/adapter/opencodezen"
	"github.com/ks1686/peaproxy/internal/adapter/openrouter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

// AccountPreset is a one-click UI/CLI account template.
type AccountPreset struct {
	ID              string `json:"id"`
	Adapter         string `json:"adapter"`
	Label           string `json:"label"`
	BaseURL         string `json:"baseURL"`
	Tier            string `json:"tier"`
	EnvKey          string `json:"envKey,omitempty"`
	EnvKeySet       bool   `json:"envKeySet"`
	AccountIDEnv    string `json:"accountIDEnv,omitempty"`
	AccountIDEnvSet bool   `json:"accountIDEnvSet"`
	URLPlaceholder  string `json:"urlPlaceholder,omitempty"`
	Warn            string `json:"warn,omitempty"`
	Note            string `json:"note,omitempty"`
}

func hostedPreset(id, label string, spec hosted.Spec) AccountPreset {
	return AccountPreset{
		ID:             id,
		Adapter:        spec.Name,
		Label:          label,
		BaseURL:        spec.DefaultBaseURL,
		Tier:           string(spec.DefaultTier),
		EnvKey:         spec.EnvKey,
		AccountIDEnv:   spec.AccountIDEnv,
		URLPlaceholder: spec.URLPlaceholder,
		Note:           spec.Notes,
	}
}

// AccountPresets is the Accounts page dropdown (and GET /admin/presets).
// EnvKeySet / AccountIDEnvSet report whether those env vars are non-empty
// in this process. Values are never included.
func AccountPresets() []AccountPreset {
	out := []AccountPreset{
		{ID: "ollama-local", Adapter: ollama.Name, Label: "Ollama (local)", BaseURL: ollama.DefaultBaseURL, Tier: string(catalog.TierLocal)},
		hostedPreset("lmstudio-local", "LM Studio (local)", hosted.LMStudio),
		hostedPreset("llamacpp-local", "llama.cpp (local)", hosted.LlamaCpp),
		hostedPreset("vllm-local", "vLLM (local)", hosted.VLLM),
		hostedPreset("jan-local", "Jan (local)", hosted.Jan),
		hostedPreset("gpt4all-local", "GPT4All (local)", hosted.GPT4All),
		hostedPreset("ollama-cloud", "Ollama Cloud", hosted.OllamaCloud),
		{ID: "anthropic-key", Adapter: anthropic.Name, Label: "Anthropic API key (official)", BaseURL: anthropic.DefaultBaseURL, Tier: string(catalog.TierPaid), EnvKey: "ANTHROPIC_API_KEY", Note: "Official Messages API. Safer than subscription OAuth."},
		{ID: "openai-key", Adapter: openai.Name, Label: "OpenAI API key (official)", BaseURL: openai.DefaultBaseURL, Tier: string(catalog.TierPaid), EnvKey: "OPENAI_API_KEY", Note: "Official Platform API. Safer than ChatGPT/Codex subscription OAuth."},
		{ID: "anthropic-oauth", Adapter: "anthropic_oauth", Label: "Claude Pro/Max (subscription OAuth)", Tier: string(catalog.TierPaid), Warn: "May violate Anthropic ToS and can ban the account. PeaProxy authors are not liable. Prefer the Anthropic API key preset. Run: peaproxy auth login --provider anthropic"},
		{ID: "openai-oauth", Adapter: "openai_oauth", Label: "ChatGPT / Codex (subscription OAuth)", Tier: string(catalog.TierPaid), Warn: "May violate OpenAI ToS and can ban the account. PeaProxy authors are not liable. Prefer the OpenAI API key preset. Run: peaproxy auth login --provider openai"},
		hostedPreset("google-key", "Google AI Studio (Gemini OpenAI-compat)", hosted.Google),
		{ID: "antigravity", Adapter: "antigravity", Label: "Gemini / Antigravity (subscription OAuth)", Tier: string(catalog.TierPaid), Warn: "May violate Google ToS and can ban the account. PeaProxy authors are not liable. Prefer the Google AI Studio API key preset. Distinct from AI Studio keys — this is Cloud Code / Antigravity consumer OAuth. Run: peaproxy auth login --provider gemini"},
		hostedPreset("groq-key", "Groq", hosted.Groq),
		hostedPreset("cerebras-key", "Cerebras", hosted.Cerebras),
		hostedPreset("xai-key", "xAI Grok (API key, official)", hosted.XAI),
		{ID: "xai-oauth", Adapter: "xai_oauth", Label: "xAI Grok (subscription OAuth)", Tier: string(catalog.TierPaid), Warn: "May violate xAI ToS and can ban the account. PeaProxy authors are not liable. Prefer the xAI API key preset. Run: peaproxy auth login --provider xai"},
		{ID: "kimi-oauth", Adapter: "kimi_oauth", Label: "Kimi / Moonshot (subscription OAuth)", Tier: string(catalog.TierPaid), Warn: "May violate Moonshot ToS and can ban the account. PeaProxy authors are not liable. Prefer an official Kimi API key via custom OpenAI-compat. Run: peaproxy auth login --provider kimi"},
		{ID: "kimi-ai-oauth", Adapter: "kimi_ai_oauth", Label: "Kimi.ai (subscription OAuth)", Tier: string(catalog.TierPaid), Warn: "May violate Moonshot ToS and can ban the account. PeaProxy authors are not liable. Run: peaproxy auth login --provider kimi-ai"},
		{ID: "meta-oauth", Adapter: "meta_oauth", Label: "Meta Muse (subscription OAuth)", Tier: string(catalog.TierPaid), Warn: "May violate Meta ToS and can ban the account. PeaProxy authors are not liable. Run: peaproxy auth login --provider meta"},
		{ID: "qwen-oauth", Adapter: "qwen_oauth", Label: "Qwen consumer OAuth (not yet)", Tier: string(catalog.TierPaid), Warn: "Not yet: CLIProxyAPI has no working Qwen consumer OAuth flow. Use a Qwen API key with the custom OpenAI-compat preset. PeaProxy authors are not liable for any future Qwen OAuth path either."},
		hostedPreset("huggingface", "Hugging Face router", hosted.HuggingFace),
		hostedPreset("nim-key", "NVIDIA NIM (API catalog)", hosted.NIM),
		hostedPreset("workers-ai", "Cloudflare Workers AI", hosted.WorkersAI),
		hostedPreset("sambanova-key", "SambaNova Cloud", hosted.SambaNova),
		{ID: "openrouter", Adapter: openrouter.Name, Label: "OpenRouter", BaseURL: openrouter.DefaultBaseURL, Tier: string(catalog.TierFreemium), EnvKey: "OPENROUTER_API_KEY", Note: "Model ids ending :free are tagged free automatically."},
		{ID: "opencode-zen", Adapter: opencodezen.Name, Label: "OpenCode Zen", BaseURL: opencodezen.DefaultBaseURL, Tier: string(catalog.TierFree), EnvKey: "OPENCODE_API_KEY", Warn: "OpenCode Zen free models may train on prompts (Nemotron, Big Pickle, MiMo, Muse). Prefer an official API key from opencode.ai."},
		{ID: "custom", Adapter: "openai_compat", Label: "Custom OpenAI-compat", BaseURL: "https://api.example.com/v1", Tier: string(catalog.TierPaid)},
	}
	for i := range out {
		out[i].EnvKeySet = hosted.EnvPresent(out[i].EnvKey)
		out[i].AccountIDEnvSet = hosted.EnvPresent(out[i].AccountIDEnv)
	}
	return out
}

// LookupPreset returns the Accounts dropdown template with the given id.
func LookupPreset(id string) (AccountPreset, bool) {
	for _, p := range AccountPresets() {
		if p.ID == id {
			return p, true
		}
	}
	return AccountPreset{}, false
}
