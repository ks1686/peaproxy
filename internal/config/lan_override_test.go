package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docs/CONFIG.md: precedence is file < PEAPROXY_* env < flags, and a LAN bind
// may take its admin token from PEAPROXY_ADMIN_TOKEN or --admin-token. Load
// validated the tokenless file before env or flags applied, so that documented
// setup could never start. The bind/token posture is checked on the final
// config (serve does), not on the file alone.
func TestLANBindCanTakeItsAdminTokenFromTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	if err := os.WriteFile(path, []byte("schemaVersion: 1\nbind: 0.0.0.0\nport: 8317\nallowNonLoopback: true\nproviders: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PEAPROXY_ADMIN_TOKEN", "from-env")
	cfg, _, _, err := EnsureFile(path)
	if err != nil {
		t.Fatalf("EnsureFile refused a LAN file whose token comes from the environment: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("effective config with the env token is invalid: %v", err)
	}
}

func TestLANBindWithoutAnyTokenStillFailsValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	if err := os.WriteFile(path, []byte("schemaVersion: 1\nbind: 0.0.0.0\nport: 8317\nallowNonLoopback: true\nproviders: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PEAPROXY_ADMIN_TOKEN", "")
	cfg, _, _, err := EnsureFile(path)
	if err == nil {
		err = cfg.Validate()
	}
	if err == nil {
		t.Fatal("a LAN bind with no admin token anywhere validated")
	}
}

// A server whose LAN token comes from the environment must still be able to
// save, and must not write that token into the file.
func TestSaveMergedWorksWhenTheLANTokenComesFromTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	if err := os.WriteFile(path, []byte("schemaVersion: 1\nbind: 0.0.0.0\nport: 8317\nallowNonLoopback: true\nproviders: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PEAPROXY_ADMIN_TOKEN", "from-env")
	running, _, _, err := EnsureFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mine := Clone(running)
	mine.Hide.Models = []string{"m"}
	if _, err := SaveMerged(path, running, mine); err != nil {
		t.Fatalf("save refused for a server whose admin token comes from the environment: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "from-env") {
		t.Fatalf("the environment's admin token was written into the config file:\n%s", b)
	}
}
