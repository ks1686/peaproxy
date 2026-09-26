package adapters

import "testing"

func TestOAuthAdapterNameAliases(t *testing.T) {
	cases := map[string]string{
		"anthropic":   "anthropic_oauth",
		"claude":      "anthropic_oauth",
		"openai":      "openai_oauth",
		"codex":       "openai_oauth",
		"gemini":      "antigravity",
		"antigravity": "antigravity",
		"xai":         "xai_oauth",
		"grok":        "xai_oauth",
		"kimi":        "kimi_oauth",
		"kimi-ai":     "kimi_ai_oauth",
		"meta":        "meta_oauth",
		"muse":        "meta_oauth",
		"qwen":        "qwen_oauth",
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
	if !IsOAuthAdapter("antigravity") || !IsOAuthAdapter("qwen_oauth") || IsOAuthAdapter("google") {
		t.Fatal("IsOAuthAdapter")
	}
	if CLIProvider("antigravity") != "gemini" || CLIProvider("kimi_ai_oauth") != "kimi-ai" {
		t.Fatal("CLIProvider")
	}
}
