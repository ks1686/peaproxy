package hosted

import (
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/catalog"
)

func TestKnownSpecsHaveBaseURLAndTier(t *testing.T) {
	want := map[string]struct {
		url  string
		tier catalog.Tier
	}{
		"lmstudio":     {url: "http://127.0.0.1:1234/v1", tier: catalog.TierLocal},
		"llamacpp":     {url: "http://127.0.0.1:8080/v1", tier: catalog.TierLocal},
		"vllm":         {url: "http://127.0.0.1:8000/v1", tier: catalog.TierLocal},
		"groq":         {url: "https://api.groq.com/openai/v1", tier: catalog.TierFreemium},
		"cerebras":     {url: "https://api.cerebras.ai/v1", tier: catalog.TierFreemium},
		"google":       {url: "https://generativelanguage.googleapis.com/v1beta/openai", tier: catalog.TierFreemium},
		"xai":          {url: "https://api.x.ai/v1", tier: catalog.TierPaid},
		"huggingface":  {url: "https://router.huggingface.co/v1", tier: catalog.TierFreemium},
		"nim":          {url: "https://integrate.api.nvidia.com/v1", tier: catalog.TierFreemium},
		"workers_ai":   {url: "https://api.cloudflare.com/client/v4/accounts/YOUR_ACCOUNT_ID/ai/v1", tier: catalog.TierFreemium},
		"ollama_cloud": {url: "https://ollama.com/v1", tier: catalog.TierFreemium},
	}
	for _, spec := range All() {
		got, ok := want[spec.Name]
		if !ok {
			t.Fatalf("unexpected spec %s", spec.Name)
		}
		if spec.DefaultBaseURL != got.url {
			t.Fatalf("%s base URL %s", spec.Name, spec.DefaultBaseURL)
		}
		if spec.DefaultTier != got.tier {
			t.Fatalf("%s tier %s", spec.Name, spec.DefaultTier)
		}
		delete(want, spec.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing specs %#v", want)
	}
}

func TestWrapFillsDefaultBaseURL(t *testing.T) {
	a, err := Wrap(LMStudio)(adapter.Options{ID: "lms"})
	if err != nil {
		t.Fatal(err)
	}
	named, ok := a.(*openai_compat.Adapter)
	if !ok {
		t.Fatalf("type %T", a)
	}
	if named.ID() != "lms" {
		t.Fatalf("id %s", named.ID())
	}
	caps := named.Capabilities()
	if !caps.Local || !caps.ListModels {
		t.Fatalf("caps %#v", caps)
	}
}

func TestGoogleNotesOfficialOpenAICompat(t *testing.T) {
	if Google.Notes == "" {
		t.Fatal("google notes must document OpenAI-compat vs generateContent")
	}
}

func TestNewHostedPresetsHaveDocsAndKeys(t *testing.T) {
	if NIM.EnvKey != "NVIDIA_API_KEY" {
		t.Fatalf("nim env %s", NIM.EnvKey)
	}
	if WorkersAI.EnvKey != "CLOUDFLARE_API_TOKEN" {
		t.Fatalf("workers_ai env %s", WorkersAI.EnvKey)
	}
	if !strings.Contains(WorkersAI.Notes, "YOUR_ACCOUNT_ID") {
		t.Fatalf("workers_ai must tell users to replace account id: %s", WorkersAI.Notes)
	}
	if OllamaCloud.EnvKey != "OLLAMA_API_KEY" {
		t.Fatalf("ollama_cloud env %s", OllamaCloud.EnvKey)
	}
	if OllamaCloud.DefaultBaseURL == "http://127.0.0.1:11434/v1" {
		t.Fatal("ollama_cloud must not reuse local Ollama")
	}
	if LlamaCpp.DefaultBaseURL == VLLM.DefaultBaseURL {
		t.Fatal("llama.cpp and vLLM default ports must differ")
	}
}
