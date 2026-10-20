package secretstore

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGetFallsBackToUnlockedReadInReadOnlyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not block file creation on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	s, err := OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("acct", KindAPIKey, "sk-readonly"); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, LockFileName)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if v, err := s.Get("acct", KindAPIKey); err != nil || v != "sk-readonly" {
		t.Fatalf("Get in a read-only dir = %q, %v", v, err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("%s exists after a read-only Get (stat err %v)", LockFileName, err)
	}
	if err := s.Set("acct", KindAPIKey, "sk-other"); err == nil {
		t.Fatal("Set succeeded without the lock in a read-only dir")
	}
}

// A config directory PeaProxy cannot write to cannot be migrated, so it is
// read through the legacy store as it stands.
func TestAnUnmigratedReadOnlyDirReadsTheLegacyStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not block file creation on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	legacy, err := OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Set("acct", KindAPIKey, "sk-legacy"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, LockFileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	s, err := OpenFor(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("OpenFor on a read-only, unmigrated dir: %v", err)
	}
	if v, err := s.Get("acct", KindAPIKey); err != nil || v != "sk-legacy" {
		t.Fatalf("Get = %q, %v; want the legacy secret", v, err)
	}
}
