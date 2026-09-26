package quota

import "testing"

func TestFormatOmitsUnknownAndKeepsZero(t *testing.T) {
	unknown := Snapshot{Note: "not reported by provider"}
	if unknown.Format() != "not reported by provider" {
		t.Fatalf("%s", unknown.Format())
	}
	if unknown.Compact() != "" {
		t.Fatalf("compact must stay empty when unreported, got %q", unknown.Compact())
	}
	z := int64(0)
	zero := Snapshot{RemainingRequests: &z}
	if zero.Format() != "remainingRequests=0" {
		t.Fatalf("%s", zero.Format())
	}
	if zero.Compact() != "req=0" {
		t.Fatalf("zero remaining must be shown, not omitted: %q", zero.Compact())
	}
	credits := 74.5
	c := Snapshot{RemainingCredits: &credits, CreditsUnlimited: false}
	if c.Format() != "remainingCredits=74.5" {
		t.Fatalf("%s", c.Format())
	}
	if c.Compact() != "credits=74.5" {
		t.Fatalf("%s", c.Compact())
	}
	u := Snapshot{CreditsUnlimited: true}
	if u.Format() != "credits=unlimited" {
		t.Fatalf("%s", u.Format())
	}
	if u.Compact() != "credits=unlimited" {
		t.Fatalf("%s", u.Compact())
	}
	req := int64(59)
	tok := int64(149984)
	both := Snapshot{RemainingRequests: &req, RemainingTokens: &tok}
	if both.Compact() != "req=59 tok=149984" {
		t.Fatalf("%s", both.Compact())
	}
}

func TestKnownAdaptersHaveFamilies(t *testing.T) {
	for _, name := range []string{
		"openai", "anthropic", "google", "gemini", "xai", "groq", "cerebras",
		"huggingface", "nim", "sambanova", "workers_ai", "ollama_cloud",
		"openrouter", "opencode_zen", "opencode_go", "openai_compat",
		"ollama", "lmstudio", "llamacpp", "vllm", "jan", "gpt4all",
		"anthropic_oauth", "openai_oauth", "antigravity", "gemini_oauth",
		"xai_oauth", "kimi_oauth", "kimi_ai_oauth", "meta_oauth",
		"copilot_oauth", "qwen_oauth", "factory_oauth",
	} {
		if _, ok := families[name]; !ok {
			t.Errorf("missing quota family for %s", name)
		}
	}
	if Lookup("openrouter").Probe == "" {
		t.Fatal("openrouter must probe GET /key")
	}
	if Lookup("copilot_oauth").Probe != "" || Lookup("openai").Probe != "" {
		t.Fatal("only OpenRouter has a documented remaining GET")
	}
}
