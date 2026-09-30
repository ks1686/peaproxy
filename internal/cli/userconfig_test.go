package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ks1686/peaproxy/internal/config"
)

// isolateUserConfig points os.UserConfigDir at a temp dir, so a command run
// without --config reads and locks nothing in the real user config dir.
func isolateUserConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	for _, k := range []string{"PEAPROXY_BIND", "PEAPROXY_PORT"} {
		t.Setenv(k, "")
	}
}

func TestIsolateUserConfigMovesDefaultPath(t *testing.T) {
	userPath := config.DefaultPath()
	isolateUserConfig(t)
	if got := config.DefaultPath(); got == userPath {
		t.Fatalf("DefaultPath still %s", got)
	}
	if _, err := os.Stat(filepath.Dir(config.DefaultPath())); !os.IsNotExist(err) {
		t.Fatalf("isolated config dir already exists (stat err %v)", err)
	}
}
