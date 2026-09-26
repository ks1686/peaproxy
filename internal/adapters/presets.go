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
	ID      string `json:"id"`
	Adapter string `json:"adapter"`
	Label   string `json:"label"`
	BaseURL string `json:"baseURL"`
	Tier    string `json:"tier"`
	EnvKey  string `json:"envKey,omitempty"`
	Warn    string `json:"warn,omitempty"`
	Note    string `json:"note,omitempty"`
}

// AccountPresets is the Accounts page dropdown (and GET /admin/presets).
func AccountPresets() []AccountPreset {
	out := []AccountPreset{
		{ID: "ollama-local", Adapter: ollama.Name, Label: "Ollama (local)", BaseURL: ollama.DefaultBaseURL, Tier: string(catalog.TierLocal)},
		{ID: "lmstudio-local", Adapter: hosted.LMStudio.Name, Label: "LM Studio (local)", BaseURL: hosted.LMStudio.DefaultBaseURL, Tier: string(catalog.TierLocal)},
		{ID: "anthropic-key", Adapter: anthropic.Name, Label: "Anthropic API key", BaseURL: anthropic.DefaultBaseURL, Tier: string(catalog.TierPaid), EnvKey: "ANTHROPIC_API_KEY"},
		{ID: "openai-key", Adapter: openai.Name, Label: "OpenAI API key", BaseURL: openai.DefaultBaseURL, Tier: string(catalog.TierPaid), EnvKey: "OPENAI_API_KEY"},
		{ID: "google-key", Adapter: hosted.Google.Name, Label: "Google AI Studio (Gemini OpenAI-compat)", BaseURL: hosted.Google.DefaultBaseURL, Tier: string(catalog.TierFreemium), EnvKey: hosted.Google.EnvKey, Note: hosted.Google.Notes},
		{ID: "groq-key", Adapter: hosted.Groq.Name, Label: "Groq", BaseURL: hosted.Groq.DefaultBaseURL, Tier: string(catalog.TierFreemium), EnvKey: hosted.Groq.EnvKey},
		{ID: "cerebras-key", Adapter: hosted.Cerebras.Name, Label: "Cerebras", BaseURL: hosted.Cerebras.DefaultBaseURL, Tier: string(catalog.TierFreemium), EnvKey: hosted.Cerebras.EnvKey},
		{ID: "xai-key", Adapter: hosted.XAI.Name, Label: "xAI Grok", BaseURL: hosted.XAI.DefaultBaseURL, Tier: string(catalog.TierPaid), EnvKey: hosted.XAI.EnvKey},
		{ID: "huggingface", Adapter: hosted.HuggingFace.Name, Label: "Hugging Face router", BaseURL: hosted.HuggingFace.DefaultBaseURL, Tier: string(catalog.TierFreemium), EnvKey: hosted.HuggingFace.EnvKey, Note: hosted.HuggingFace.Notes},
		{ID: "openrouter", Adapter: openrouter.Name, Label: "OpenRouter", BaseURL: openrouter.DefaultBaseURL, Tier: string(catalog.TierFreemium), EnvKey: "OPENROUTER_API_KEY", Note: "Model ids ending :free are tagged free automatically."},
		{ID: "opencode-zen", Adapter: opencodezen.Name, Label: "OpenCode Zen", BaseURL: opencodezen.DefaultBaseURL, Tier: string(catalog.TierFree), EnvKey: "OPENCODE_API_KEY", Warn: "OpenCode Zen free models may train on prompts (Nemotron, Big Pickle, MiMo, Muse). Prefer an official API key from opencode.ai."},
		{ID: "custom", Adapter: "openai_compat", Label: "Custom OpenAI-compat", BaseURL: "https://api.example.com/v1", Tier: string(catalog.TierPaid)},
	}
	return out
}
