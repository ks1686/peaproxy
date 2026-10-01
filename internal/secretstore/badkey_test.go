package secretstore

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// #59: a wrong-sized secret.key used to spin through the whole retry budget
// under both locks before failing, and the error said only "must be 32 bytes":
// no path, no length, no way out.
func TestWrongSizedKeyFileFailsWithAnActionableError(t *testing.T) {
	dir := t.TempDir()
	s := &Store{backend: BackendFile, dir: dir}
	keyPath := filepath.Join(dir, KeyFileName)
	const garbage = "too short"
	if err := os.WriteFile(keyPath, []byte(garbage), 0o600); err != nil {
		t.Fatal(err)
	}
	// Older than the grace period, so this is a broken file and not one being
	// written by a racing first run.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(keyPath, old, old); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := s.loadOrCreateKey()
	if err == nil {
		t.Fatal("expected an error")
	}
	elapsed := time.Since(start)
	msg := err.Error()
	for _, want := range []string{keyPath, fmt.Sprintf("%d bytes", len(garbage)), "32"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
	// Something that tells the user what to do about it.
	if !strings.Contains(msg, "delete") && !strings.Contains(msg, "remove") {
		t.Errorf("error offers no recovery step: %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("a settled bad key took %v to fail; the retry budget should not be spent on it", elapsed)
	}
}

// A key file that is young and the wrong size is a file another process is
// still writing, so the shared read keeps its grace period rather than failing.
// The locked read is the mirror image: see grace_test.go (#64).
func TestYoungWrongSizedKeyFileStillWaits(t *testing.T) {
	dir := t.TempDir()
	s := &Store{backend: BackendFile, dir: dir}
	keyPath := filepath.Join(dir, KeyFileName)
	if err := os.WriteFile(keyPath, make([]byte, 4), 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = os.WriteFile(keyPath, make([]byte, 32), 0o600)
	}()
	key, err := s.loadOrCreateKeyShared()
	if err != nil {
		t.Fatalf("a key being written should have been waited for: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("read %d bytes, want 32", len(key))
	}
}

// A 0-byte key file is the classic "disk full mid-write" or "interrupted"
// state, and once it is clearly not in flight it must not be retried either.
func TestZeroByteKeyFileFailsQuickly(t *testing.T) {
	dir := t.TempDir()
	s := &Store{backend: BackendFile, dir: dir}
	keyPath := filepath.Join(dir, KeyFileName)
	if err := os.WriteFile(keyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(keyPath, old, old); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := s.loadOrCreateKey()
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "0 bytes") {
		t.Errorf("error does not say the file is empty: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("an empty key file took %v to fail", elapsed)
	}
}

// The error must not suggest destroying the user's secrets without saying what
// that costs: every stored secret is encrypted with this key.
func TestBadKeyErrorWarnsAboutWhatIsEncryptedWithIt(t *testing.T) {
	dir := t.TempDir()
	s := &Store{backend: BackendFile, dir: dir}
	keyPath := filepath.Join(dir, KeyFileName)
	if err := os.WriteFile(keyPath, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(keyPath, old, old); err != nil {
		t.Fatal(err)
	}
	_, err := s.loadOrCreateKey()
	if err == nil {
		t.Fatal("expected an error")
	}
	if runtime.GOOS == "windows" {
		t.Skip("path format differs")
	}
	if !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("error does not say the stored secrets are encrypted with this key: %v", err)
	}
}
