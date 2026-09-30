package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ks1686/peaproxy/internal/secretstore"
)

// readOnlyDir makes dir read-only for the test and restores it afterwards.
func readOnlyDir(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not block file creation on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

func TestReadsNeedNoWriteAccessToConfigDir(t *testing.T) {
	t.Setenv("PEAPROXY_SECRET_BACKEND", "file")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := Default()
	cfg.Providers[0].APIKey = "sk-readonly"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.lock", secretstore.LockFileName} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	readOnlyDir(t, dir)

	got, _, created, err := EnsureFile(path)
	if err != nil || created {
		t.Fatalf("EnsureFile on a read-only dir: created=%v err=%v", created, err)
	}
	if key := got.Providers[0].APIKey; key != "sk-readonly" {
		t.Fatalf("EnsureFile api key %q, want sk-readonly", key)
	}
	if got, err := Load(path); err != nil || got.Providers[0].APIKey != "sk-readonly" {
		t.Fatalf("Load on a read-only dir: %v (key %q)", err, got.Providers[0].APIKey)
	}
	for _, name := range []string{"config.lock", secretstore.LockFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s exists after read-only reads (stat err %v)", name, err)
		}
	}
}

func TestEnsureFileAppliesEnvToExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PEAPROXY_PORT", "9911")
	got, _, created, err := EnsureFile(path)
	if err != nil || created {
		t.Fatalf("EnsureFile: created=%v err=%v", created, err)
	}
	if got.Port != 9911 {
		t.Fatalf("port %d, want env overlay 9911", got.Port)
	}
	if disk := loadOrFatal(t, path); disk.Port == 9911 {
		t.Fatal("env overlay was written to the file")
	}
}

func loadOrFatal(t *testing.T, path string) Config {
	t.Helper()
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
