package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #134: `peaproxy clients import pi` reads the live catalog and writes an owned
// provider into Pi's models.json, end to end through the command.
func TestClientsImportPiWritesOwnedAccountProvider(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/catalog" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("filter") != "all" {
			t.Errorf("filter = %q, want all", r.URL.Query().Get("filter"))
		}
		_, _ = w.Write([]byte(`{"models":[{"id":"gemini-3.8-flash","accountId":"work","modalities":["text","image"],"contextWindow":1048576,"routable":true},{"id":"claude-sonnet-5","accountId":"work","modalities":["text","image"],"routable":true}]}`))
	}))
	t.Cleanup(gw.Close)

	home := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "pi"))
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"clients", "import", "pi", "--account", "work", "--origin", gw.URL}, out)
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(home, "pi", "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"peaproxy-work"`) || !strings.Contains(s, "gemini-3.8-flash") {
		t.Fatalf("owned provider not written:\n%s", s)
	}
	if strings.Contains(s, "claude-sonnet-5") {
		t.Fatalf("built-in Claude model was imported:\n%s", s)
	}
	if !strings.Contains(s, `"input":["text","image"]`) || !strings.Contains(s, `"contextWindow":1048576`) {
		t.Fatalf("Pi model metadata was not imported:\n%s", s)
	}
}

func TestClientsImportRequiresAccount(t *testing.T) {
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"clients", "import", "pi"}, out); err == nil {
		t.Fatal("expected --account to be required")
	}
}
