package adapters

import (
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter/hosted"
)

func TestAccountPresetsIncludeHosted(t *testing.T) {
	got := map[string]AccountPreset{}
	for _, p := range AccountPresets() {
		got[p.ID] = p
	}
	for _, id := range []string{
		"ollama-local", "lmstudio-local", "google-key", "groq-key", "cerebras-key",
		"xai-key", "huggingface", "openrouter", "custom", "anthropic-oauth", "openai-oauth",
		"antigravity", "xai-oauth", "kimi-oauth", "kimi-ai-oauth", "meta-oauth", "qwen-oauth",
	} {
		if _, ok := got[id]; !ok {
			t.Fatalf("missing preset %s", id)
		}
	}
	if got["lmstudio-local"].Adapter != hosted.LMStudio.Name {
		t.Fatalf("lmstudio adapter %s", got["lmstudio-local"].Adapter)
	}
	if got["google-key"].BaseURL != hosted.Google.DefaultBaseURL {
		t.Fatalf("google url %s", got["google-key"].BaseURL)
	}
	if got["anthropic-oauth"].Adapter != "anthropic_oauth" || got["anthropic-oauth"].Warn == "" {
		t.Fatalf("oauth preset: %#v", got["anthropic-oauth"])
	}
	if got["antigravity"].Adapter != "antigravity" || got["antigravity"].Warn == "" {
		t.Fatalf("antigravity preset: %#v", got["antigravity"])
	}
	if got["qwen-oauth"].Adapter != "qwen_oauth" || got["qwen-oauth"].Warn == "" {
		t.Fatalf("qwen stub: %#v", got["qwen-oauth"])
	}
}
