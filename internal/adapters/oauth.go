package adapters

import (
	"fmt"
	"strings"
)

// OAuthAdapterName maps a CLI --provider value to a registry factory name.
func OAuthAdapterName(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "anthropic_oauth", "claude":
		return "anthropic_oauth", nil
	case "openai", "openai_oauth", "chatgpt", "codex":
		return "openai_oauth", nil
	case "gemini", "antigravity", "gemini_oauth", "google-oauth", "google_oauth":
		return "antigravity", nil
	case "xai", "grok", "xai_oauth":
		return "xai_oauth", nil
	case "kimi", "kimi_oauth", "moonshot":
		return "kimi_oauth", nil
	case "kimi-ai", "kimi.ai", "kimi_ai_oauth":
		return "kimi_ai_oauth", nil
	case "meta", "muse", "meta_oauth":
		return "meta_oauth", nil
	case "qwen", "qwen_oauth":
		return "", fmt.Errorf("%s", qwenDiscontinued)
	case "copilot", "github-copilot", "github_copilot", "copilot_oauth":
		return "copilot_oauth", nil
	case "kilo", "kilo_oauth", "kilocode", "kilo-code":
		return "kilo_oauth", nil
	case "minimax", "minimax_oauth", "minimax-oauth":
		return "minimax_oauth", nil
	case "factory", "droid", "factory_oauth", "factory-droid":
		return "factory_oauth", nil
	case "opencode-go", "opencode_go", "opencodego":
		return "opencode_go", nil
	default:
		return "", fmt.Errorf("unknown OAuth provider %q (anthropic | openai | gemini | xai | kimi | kimi-ai | meta | copilot | kilo | minimax | factory | opencode-go)", provider)
	}
}

// qwenDiscontinued is returned for the removed Qwen login. The factory stays
// registered so a saved qwen_oauth row fails with this text.
const qwenDiscontinued = "qwen_oauth: Qwen free-tier consumer OAuth was discontinued on 2026-04-15; use an API key or the Alibaba Coding Plan preset"

// DefaultOAuthAccountID is the YAML provider id written by auth login.
func DefaultOAuthAccountID(adapterName string) string {
	switch adapterName {
	case "anthropic_oauth":
		return "anthropic-oauth"
	case "openai_oauth":
		return "openai-oauth"
	case "antigravity", "gemini_oauth":
		return "antigravity"
	case "xai_oauth":
		return "xai-oauth"
	case "kimi_oauth":
		return "kimi-oauth"
	case "kimi_ai_oauth":
		return "kimi-ai-oauth"
	case "meta_oauth":
		return "meta-oauth"
	case "copilot_oauth":
		return "copilot-oauth"
	case "kilo_oauth":
		return "kilo-oauth"
	case "minimax_oauth":
		return "minimax-oauth"
	case "factory_oauth":
		return "factory-oauth"
	case "opencode_go":
		return "opencode-go"
	default:
		return adapterName
	}
}

// CLIProvider is the --provider value to copy from the Accounts UI.
func CLIProvider(adapterName string) string {
	switch adapterName {
	case "openai_oauth":
		return "openai"
	case "anthropic_oauth":
		return "anthropic"
	case "antigravity", "gemini_oauth":
		return "gemini"
	case "xai_oauth":
		return "xai"
	case "kimi_oauth":
		return "kimi"
	case "kimi_ai_oauth":
		return "kimi-ai"
	case "meta_oauth":
		return "meta"
	case "copilot_oauth":
		return "copilot"
	case "kilo_oauth":
		return "kilo"
	case "minimax_oauth":
		return "minimax"
	case "factory_oauth":
		return "factory"
	case "opencode_go":
		return "opencode-go"
	default:
		return adapterName
	}
}

// IsOAuthAdapter reports whether the factory is a subscription-OAuth implementation.
func IsOAuthAdapter(name string) bool {
	switch name {
	case "anthropic_oauth", "openai_oauth", "antigravity", "gemini_oauth",
		"xai_oauth", "kimi_oauth", "kimi_ai_oauth", "meta_oauth",
		"copilot_oauth", "kilo_oauth", "minimax_oauth", "factory_oauth":
		return true
	default:
		return false
	}
}
