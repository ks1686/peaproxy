package clients

import (
	"errors"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func connectContinue(t *testing.T, original string) (Layout, string, string, error) {
	t.Helper()
	root := t.TempDir()
	layout := Layout{Root: root}
	path := layout.path("continue")
	writeAt(t, path, original)
	err := layout.Connect("continue", "http://127.0.0.1:8317/v1", "pea/auto")
	return layout, path, readAt(t, path), err
}

func continueModels(t *testing.T, text string) []map[string]any {
	t.Helper()
	var doc struct {
		Models []map[string]any `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("connect wrote invalid YAML: %v\n%s", err, text)
	}
	return doc.Models
}

// The scanner only knew a line reading exactly "models:", so an empty flow
// list or a header with a comment got a second top-level models key appended.
func TestContinueConnectReusesAnExistingModelsKey(t *testing.T) {
	for name, original := range map[string]string{
		"empty flow list":   "name: me\nmodels: []\n",
		"header comment":    "name: me\nmodels: # mine\n  - name: Local\n    provider: ollama\n    model: llama3\n",
		"nested models key": "name: me\nother:\n  models:\n    - x\n",
	} {
		t.Run(name, func(t *testing.T) {
			layout, path, connected, err := connectContinue(t, original)
			if err != nil {
				t.Fatal(err)
			}
			if n := strings.Count("\n"+connected, "\nmodels:"); n != 1 {
				t.Fatalf("%d top-level models keys after connect:\n%s", n, connected)
			}
			found := false
			for _, m := range continueModels(t, connected) {
				if m["name"] == "PeaProxy" {
					found = true
				}
			}
			if !found {
				t.Fatalf("PeaProxy model missing from the top-level models list:\n%s", connected)
			}
			if err := layout.Disconnect("continue"); err != nil {
				t.Fatal(err)
			}
			if got := readAt(t, path); got != original {
				t.Fatalf("round trip changed the file:\n--- got ---\n%q\n--- want ---\n%q", got, original)
			}
		})
	}
}

func TestContinueConnectRefusesAFlowListItCannotMerge(t *testing.T) {
	original := "models: [{name: Local, provider: ollama, model: llama3}]\n"
	_, path, _, err := connectContinue(t, original)
	if !errors.Is(err, ErrGuidedSetup) {
		t.Fatalf("connect = %v, want ErrGuidedSetup for a flow list", err)
	}
	if b, _ := os.ReadFile(path); string(b) != original {
		t.Fatalf("a refused connect changed the file:\n%s", b)
	}
}
