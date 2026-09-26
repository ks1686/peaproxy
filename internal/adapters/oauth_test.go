package adapters

import "testing"

func TestOAuthAdapterNameAliases(t *testing.T) {
	cases := map[string]string{
		"anthropic":      "anthropic_oauth",
		"claude":         "anthropic_oauth",
		"openai":         "openai_oauth",
		"codex":          "openai_oauth",
		"gemini":         "antigravity",
		"antigravity":    "antigravity",
		"xai":            "xai_oauth",
		"grok":           "xai_oauth",
		"kimi":           "kimi_oauth",
		"kimi-ai":        "kimi_ai_oauth",
		"meta":           "meta_oauth",
		"muse":           "meta_oauth",
		"qwen":           "qwen_oauth",
		"copilot":        "copilot_oauth",
		"github-copilot": "copilot_oauth",
		"factory":        "factory_oauth",
		"droid":          "factory_oauth",
		"opencode-go":    "opencode_go",
	}
	for in, want := range cases {
		got, err := OAuthAdapterName(in)
		if err != nil || got != want {
			t.Fatalf("%s: got %s %v want %s", in, got, err, want)
		}
	}
	if _, err := OAuthAdapterName("nope"); err == nil {
		t.Fatal("expected error")
	}
	if !IsOAuthAdapter("antigravity") || !IsOAuthAdapter("qwen_oauth") || !IsOAuthAdapter("copilot_oauth") || !IsOAuthAdapter("factory_oauth") || IsOAuthAdapter("google") || IsOAuthAdapter("opencode_go") {
		t.Fatal("IsOAuthAdapter")
	}
	if CLIProvider("antigravity") != "gemini" || CLIProvider("kimi_ai_oauth") != "kimi-ai" || CLIProvider("copilot_oauth") != "copilot" || CLIProvider("opencode_go") != "opencode-go" {
		t.Fatal("CLIProvider")
	}
}
