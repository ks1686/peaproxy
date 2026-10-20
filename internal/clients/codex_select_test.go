package clients

import (
	"path/filepath"
	"strings"
	"testing"
)

// Codex reads model_provider and model from the top level of config.toml; a
// model key inside [model_providers.peaproxy] is ignored. Connect wrote only
// the provider table, so Codex kept its previous provider and model.
func TestCodexConnectSelectsThePeaProxyProviderAndModel(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".codex", "config.toml")
	original := "model_provider = \"openai\"\nmodel = \"gpt-5\" # mine\n\n[tools]\nweb_search = true\n"
	writeAt(t, path, original)
	layout := Layout{Root: root}
	if err := layout.Connect("codex", "http://127.0.0.1:8317/v1", "pea/auto"); err != nil {
		t.Fatal(err)
	}
	connected := readAt(t, path)
	top := connected
	if i := strings.Index(connected, "\n["); i >= 0 {
		top = connected[:i]
	}
	effective := map[string]string{}
	for _, line := range strings.Split(top, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		effective[strings.TrimSpace(k)] = v
	}
	if effective["model_provider"] != `"peaproxy"` || effective["model"] != `"pea/auto"` {
		t.Fatalf("top-level selection after connect = %v, want model_provider \"peaproxy\" and model \"pea/auto\":\n%s", effective, connected)
	}
	if !strings.Contains(connected, "[model_providers.peaproxy]") || !strings.Contains(connected, `wire_api = "responses"`) {
		t.Fatalf("provider table missing or without wire_api:\n%s", connected)
	}
	if err := layout.Disconnect("codex"); err != nil {
		t.Fatal(err)
	}
	if got := readAt(t, path); got != original {
		t.Fatalf("disconnect did not restore the previous selection:\n--- got ---\n%q\n--- want ---\n%q", got, original)
	}
}
