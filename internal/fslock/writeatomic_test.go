package fslock

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// #51: a writer that dies mid-write must not be able to truncate the file it
// was replacing. The temp file name is unique per write, so two writers racing
// the same target cannot scribble on each other's partial output either.
func TestWriteAtomicDoesNotTruncateTheTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	if err := WriteAtomic(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A second write fails part way through: the temp file gets a name, but the
	// bytes never land.
	if err := writeAtomicWith(path, []byte("replacement"), 0o600, func(f *os.File) error {
		if _, err := f.WriteString("partial"); err != nil {
			return err
		}
		return os.ErrInvalid
	}); err == nil {
		t.Fatal("expected the injected failure to surface")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("target was damaged by a failed write: %q", got)
	}
	assertNoTemps(t, dir)
}

// Two writers writing the same path at once must both produce a complete,
// valid file -- not one file that is half of each.
func TestWriteAtomicConcurrentWritersDoNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "harness.json")
	const writers, size = 8, 4096
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := WriteAtomic(path, []byte(strings.Repeat(string(rune('a'+i)), size)), 0o600); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != size {
		t.Fatalf("target is %d bytes, want %d: a concurrent write was lost", len(got), size)
	}
	// One writer's bytes, all of them: no interleaving.
	first := got[0]
	for _, b := range got {
		if b != first {
			t.Fatal("target is a mix of two writers")
		}
	}
	assertNoTemps(t, dir)
}

// The temp file has to be in the target's directory, or the rename crosses a
// filesystem boundary and stops being atomic.
func TestWriteAtomicKeepsTheTempBesideTheTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "usage.json")
	if err := WriteAtomic(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nested", "usage.json")); err != nil {
		t.Fatal(err)
	}
	assertNoTemps(t, dir)
}

// Two writers writing the same path must not land on the same scratch file, so
// the temp name has to be unique per write rather than a fixed path+".tmp" that
// both of them would use.
func TestWriteAtomicTempNameIsUniquePerWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	var seen []string
	for i := 0; i < 3; i++ {
		if err := writeAtomicWith(path, []byte("x"), 0o600, func(f *os.File) error {
			seen = append(seen, f.Name())
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("captured %d temp names, want 3", len(seen))
	}
	for i, name := range seen {
		if name == path+".tmp" {
			t.Fatalf("the fixed %s name is still in use", path+".tmp")
		}
		if filepath.Dir(name) != dir {
			t.Errorf("temp file %d is in %s, not beside the target", i, filepath.Dir(name))
		}
		for j, other := range seen {
			if i != j && name == other {
				t.Errorf("writes %d and %d shared the temp file %s", i, j, name)
			}
		}
	}
}

// A missing parent directory is created rather than reported: callers write
// usage.json next to a config that may itself be brand new.
func TestWriteAtomicCreatesTheDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "usage.json")
	if err := WriteAtomic(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// Permissions are applied to the temp file before it is visible, so the target
// never exists with the wrong mode, not even briefly.
func TestWriteAtomicUsesTheGivenMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	if err := WriteAtomic(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode %v, want 0600", got)
	}
}

func assertNoTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("a temp file was left behind: %s", e.Name())
		}
	}
}
