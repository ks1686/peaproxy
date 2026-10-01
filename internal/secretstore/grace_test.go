package secretstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #64: three mechanisms existed to survive a pre-v2.0.10 binary sharing the
// config directory, since v2.0.10 no binary does that. Two turned out to be
// dead weight; the third is not dead at all, but for a different reason than
// the issue assumed -- which is what these tests pin down.

func writeKey(t *testing.T, dir string, b []byte) {
	t.Helper()
	if err := putKey(dir, b); err != nil {
		t.Fatal(err)
	}
}

// putKey is writeKey for a goroutine: t.Fatal is not legal off the test's own
// goroutine.
func putKey(dir string, b []byte) error {
	return os.WriteFile(filepath.Join(dir, KeyFileName), b, 0o600)
}

// Every store operation holds secrets.lock for its whole duration, so a locked
// read cannot be looking at a half-written key. It must not sit in a retry
// loop waiting for one: that loop holds two locks for half a second to report
// an error the first read already proved.
func TestLockedKeyReadReportsABadKeyWithoutRetrying(t *testing.T) {
	dir := t.TempDir()
	writeKey(t, dir, []byte("short"))
	s := &Store{backend: BackendFile, dir: dir}

	start := time.Now()
	_, err := s.loadOrCreateKey()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a 5-byte key file was accepted")
	}
	// The old loop was 50 attempts of 10ms, so 500ms minimum.
	if elapsed > 250*time.Millisecond {
		t.Errorf("a locked read spent %v retrying a broken key file", elapsed)
	}
}

// Retrying is what was removed; the explanation is not. The error still has to
// name the file and say why deleting it costs the user their secrets.
func TestLockedBadKeyErrorStillExplains(t *testing.T) {
	dir := t.TempDir()
	writeKey(t, dir, []byte("short"))
	s := &Store{backend: BackendFile, dir: dir}
	_, err := s.loadOrCreateKey()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{KeyFileName, "5 bytes", "encrypted with"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q: %v", want, err)
		}
	}
}

// A fresh wrong-sized key file is exactly what another process's createKey
// looks like part-way through. Locked, that cannot happen, so the locked read
// reports it at once...
func TestLockedKeyReadDoesNotWaitForTheKeyToSettle(t *testing.T) {
	dir := t.TempDir()
	writeKey(t, dir, []byte("short"))
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = putKey(dir, []byte(strings.Repeat("k", 32)))
	}()
	s := &Store{backend: BackendFile, dir: dir}
	if _, err := s.loadOrCreateKey(); err == nil {
		t.Fatal("a locked read waited 30ms for a key file no writer can still be writing")
	}
}

// ...but Get falls back to an unlocked read when it cannot open secrets.lock
// for writing (permission, read-only filesystem), and on that path a concurrent
// creator is genuinely possible -- the fallback exists precisely because this
// process is not the only writer here. So the retry stays there.
func TestUnlockedKeyReadStillWaitsForTheKeyToSettle(t *testing.T) {
	dir := t.TempDir()
	writeKey(t, dir, []byte("short"))
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = putKey(dir, []byte(strings.Repeat("k", 32)))
	}()
	s := &Store{backend: BackendFile, dir: dir}
	key, err := s.loadOrCreateKeyShared()
	if err != nil {
		t.Fatalf("the unlocked read gave up on a key that became valid: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("got %d bytes, want 32", len(key))
	}
}

// An interrupted save leaves a pending entry behind. With every writer taking
// secrets.lock, a sweeper holding that same lock is looking at a generation
// nobody is still writing, whatever its age -- so the 30 minute grace only
// delayed reclaiming a generation already known to be dead.
func TestSweepReclaimsAnOrphanWithNoGrace(t *testing.T) {
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: newFault()}
	if err := s.Set("acct", KindOAuth, strings.Repeat("1", 5000)); err != nil {
		t.Fatal(err)
	}
	idx, err := s.loadIndex()
	if err != nil {
		t.Fatal(err)
	}
	orphan := pending{key: "acct/oauth", ref: chunkRef{gen: "deadbeef", n: 2}, at: s.clock().Unix()}
	idx.pending[orphan] = struct{}{}
	if len(idx.pending) != 1 {
		t.Fatalf("fixture is wrong: %d pending", len(idx.pending))
	}

	s.sweep(idx)

	if _, ok := idx.pending[orphan]; ok {
		t.Error("a fresh orphan survived the sweep; the grace is still being applied")
	}
	s.sweep(idx) // a second pass stays quiet
}

// Zero grace is only safe because the sweeper re-reads the header before
// deleting anything. A pending entry naming the generation that is actually
// live must lose its chunks to nobody -- the entry is forgotten, since it is no
// longer pending, but the value has to survive the sweep and still read back.
func TestSweepLeavesTheLiveGenerationAloneAtAnyAge(t *testing.T) {
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	if err := s.Set("acct", KindOAuth, strings.Repeat("1", 5000)); err != nil {
		t.Fatal(err)
	}
	idx, err := s.loadIndex()
	if err != nil {
		t.Fatal(err)
	}
	live, err := s.replaceableChunks("acct/oauth")
	if err != nil {
		t.Fatal(err)
	}
	if live.n == 0 {
		t.Skip("no chunked generation to protect")
	}
	self := pending{key: "acct/oauth", ref: live, at: s.clock().Unix()}
	idx.pending[self] = struct{}{}

	s.sweep(idx)

	if _, ok := idx.pending[self]; ok {
		t.Error("the live generation is still marked pending; it was never settled")
	}
	if n := chunkItems(kr, "acct/oauth"); n != live.n {
		t.Fatalf("the sweep deleted the live generation's chunks: %d left, want %d", n, live.n)
	}
	if got, err := s.Get("acct", KindOAuth); err != nil || got != strings.Repeat("1", 5000) {
		t.Fatalf("the live value did not survive the sweep: len=%d err=%v", len(got), err)
	}
}
