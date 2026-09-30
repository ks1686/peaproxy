package fslock

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func lockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.lock")
}

func TestLockBlocksSecondCallerInProcess(t *testing.T) {
	path := lockPath(t)
	unlockA, err := Lock(path, time.Second)
	if err != nil {
		t.Fatalf("A: %v", err)
	}

	acquired := make(chan error, 1)
	go func() {
		unlockB, err := Lock(path, 5*time.Second)
		if err == nil {
			defer unlockB()
		}
		acquired <- err
	}()

	select {
	case err := <-acquired:
		t.Fatalf("B returned while A held the lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	unlockA()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("B after A unlocked: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("B did not acquire after A unlocked")
	}
}

func TestLockBusyFromInProcessStage(t *testing.T) {
	path := lockPath(t)
	unlockA, err := Lock(path, time.Second)
	if err != nil {
		t.Fatalf("A: %v", err)
	}
	defer unlockA()

	old := tryLockCalls.Load()
	t.Cleanup(func() { tryLockCalls.Store(old) })
	tryLockCalls.Store(0)

	const timeout = 200 * time.Millisecond
	done := make(chan error, 1)
	var elapsed time.Duration
	go func() {
		start := time.Now()
		unlockB, err := Lock(path, timeout)
		elapsed = time.Since(start)
		if err == nil {
			unlockB()
		}
		done <- err
	}()
	err = <-done

	if !errors.Is(err, ErrBusy) {
		t.Fatalf("B err = %v, want ErrBusy", err)
	}
	if elapsed < timeout || elapsed > timeout+time.Second {
		t.Fatalf("B elapsed = %v, want within [%v, %v]", elapsed, timeout, timeout+time.Second)
	}
	if n := tryLockCalls.Load(); n != 0 {
		t.Fatalf("B reached the OS stage (%d tryLock calls); stage 1 should have rejected it", n)
	}
}

func TestLockIsReleasedOnUnlock(t *testing.T) {
	path := lockPath(t)
	unlock, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	unlock() // idempotent

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file must survive unlock: %v", err)
	}

	unlock, err = Lock(path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("relock after unlock: %v", err)
	}
	unlock()
}

func TestLockIsNotReentrant(t *testing.T) {
	path := lockPath(t)
	unlock, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	if _, err := Lock(path, 100*time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Lock from the same goroutine: err = %v, want ErrBusy", err)
	}
}

func TestLockSharesSemaphoreAcrossPathSpellings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")
	unlock, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	alias := filepath.Join(dir, "sub", "..", "test.lock")
	if _, err := Lock(alias, 100*time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Fatalf("Lock(%q): err = %v, want ErrBusy", alias, err)
	}
}

func TestLockCreatesFileWithMode0600(t *testing.T) {
	path := lockPath(t)
	unlock, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 0 {
		t.Fatalf("lock file size = %d, want 0", fi.Size())
	}
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("lock file mode = %v, want no group/other bits", perm)
		}
	}
}

func TestLockMissingDirFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "test.lock")
	if _, err := Lock(path, 100*time.Millisecond); err == nil || errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want an open error", err)
	}
	// The semaphore must have been released: once the dir exists the lock works.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := Lock(path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("after mkdir: %v", err)
	}
	unlock()
}

func TestRenameReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("from a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("from b"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Rename(a, b); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	got, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("from a")) {
		t.Fatalf("b = %q, want %q", got, "from a")
	}
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Fatalf("a still exists: %v", err)
	}
}

func TestRenameMissingSourceFails(t *testing.T) {
	dir := t.TempDir()
	if err := Rename(filepath.Join(dir, "nope"), filepath.Join(dir, "b")); err == nil {
		t.Fatal("Rename of a missing file succeeded")
	}
}
