package adapters

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter/hosted"
)

func TestAccountPresetsIncludeHosted(t *testing.T) {
	got := map[string]AccountPreset{}
	for _, p := range AccountPresets() {
		got[p.ID] = p
	}
	for _, id := range []string{
		"ollama-local", "lmstudio-local", "llamacpp-local", "vllm-local", "jan-local", "gpt4all-local", "ollama-cloud",
		"google-key", "groq-key", "cerebras-key",
		"xai-key", "huggingface", "nim-key", "workers-ai", "sambanova-key", "openrouter", "custom", "anthropic-oauth", "openai-oauth",
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
	if got["llamacpp-local"].BaseURL != hosted.LlamaCpp.DefaultBaseURL || got["vllm-local"].Adapter != hosted.VLLM.Name {
		t.Fatalf("local servers: %#v %#v", got["llamacpp-local"], got["vllm-local"])
	}
	if got["ollama-cloud"].BaseURL != hosted.OllamaCloud.DefaultBaseURL || got["ollama-cloud"].EnvKey != hosted.OllamaCloud.EnvKey {
		t.Fatalf("ollama cloud: %#v", got["ollama-cloud"])
	}
	if got["nim-key"].EnvKey != hosted.NIM.EnvKey || got["workers-ai"].Note == "" {
		t.Fatalf("hosted free: %#v %#v", got["nim-key"], got["workers-ai"])
	}
	if got["jan-local"].BaseURL != hosted.Jan.DefaultBaseURL || got["gpt4all-local"].Adapter != hosted.GPT4All.Name {
		t.Fatalf("desktop local: %#v %#v", got["jan-local"], got["gpt4all-local"])
	}
	if got["sambanova-key"].BaseURL != hosted.SambaNova.DefaultBaseURL || got["sambanova-key"].EnvKey != hosted.SambaNova.EnvKey {
		t.Fatalf("sambanova: %#v", got["sambanova-key"])
	}
	if got["workers-ai"].AccountIDEnv != hosted.WorkersAI.AccountIDEnv || got["workers-ai"].URLPlaceholder != hosted.WorkersAI.URLPlaceholder {
		t.Fatalf("workers-ai account-id: %#v", got["workers-ai"])
	}
	if got["groq-key"].Note == "" || got["cerebras-key"].Note == "" {
		t.Fatal("groq/cerebras presets need documented notes")
	}
}

func TestAccountPresetsReportEnvPresenceWithoutValues(t *testing.T) {
	const secret = "super-secret-groq-value-do-not-leak"
	t.Setenv("GROQ_API_KEY", secret)
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "cfacct-secret-do-not-leak")
	presets := AccountPresets()
	raw, err := json.Marshal(presets)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if strings.Contains(body, secret) || strings.Contains(body, "cfacct-secret-do-not-leak") {
		t.Fatal("preset JSON leaked an env value")
	}
	got := map[string]AccountPreset{}
	for _, p := range presets {
		got[p.ID] = p
	}
	if !got["groq-key"].EnvKeySet || got["groq-key"].EnvKey != "GROQ_API_KEY" {
		t.Fatalf("groq env discovery: %#v", got["groq-key"])
	}
	if !got["workers-ai"].AccountIDEnvSet || got["workers-ai"].AccountIDEnv != "CLOUDFLARE_ACCOUNT_ID" {
		t.Fatalf("workers-ai account-id discovery: %#v", got["workers-ai"])
	}
	if got["ollama-local"].EnvKeySet {
		t.Fatalf("local ollama should not claim a key env: %#v", got["ollama-local"])
	}
}

func TestLookupPresetByID(t *testing.T) {
	p, ok := LookupPreset("jan-local")
	if !ok || p.Adapter != hosted.Jan.Name {
		t.Fatalf("jan-local: %#v ok=%v", p, ok)
	}
	if _, ok := LookupPreset("not-a-preset"); ok {
		t.Fatal("unknown preset should miss")
	}
}
