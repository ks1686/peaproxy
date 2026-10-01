package secretstore

import (
	"os"
	"strings"
	"testing"
)

// crash runs a Set that the fake keyring kills mid-flight. A killed process
// runs none of the store's own cleanup; a panic unwinding past it models that.
func crash(t *testing.T, set func() error) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected the write to be killed")
		}
	}()
	_ = set()
}

func killOnSet(kr *faultKeyring, user string) {
	kr.onSet = func(u string) bool {
		if u != user {
			return false
		}
		panic("process killed")
	}
}

func chunkItems(kr *faultKeyring, key string) int {
	n := 0
	for k := range kr.m {
		if strings.HasPrefix(k, Service+"\x00"+key+"#") {
			n++
		}
	}
	return n
}

func TestReplacedGenerationOrphansAreSweptOnNextPrune(t *testing.T) {
	// Given: v2's header is published but the process dies before deleting v1's chunks.
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	v2 := strings.Repeat("2", 5000)
	if err := s.Set("acct", KindOAuth, strings.Repeat("1", 5000)); err != nil {
		t.Fatal(err)
	}
	kr.onDelete = func(u string) bool {
		if !strings.HasPrefix(u, "acct/oauth#") {
			return false
		}
		panic("process killed")
	}
	crash(t, func() error { return s.Set("acct", KindOAuth, v2) })
	if n := chunkItems(kr, "acct/oauth"); n != 6 {
		t.Fatalf("want 3 new + 3 replaced chunks after the crash, got %d", n)
	}

	// When: no clock jump needed any more -- the next prune reclaims at once.
	if err := s.Prune([]string{"acct"}); err != nil {
		t.Fatal(err)
	}

	// Then
	if n := chunkItems(kr, "acct/oauth"); n != 3 {
		t.Fatalf("%d replaced chunks survived the next prune", n-3)
	}
	if got, err := s.Get("acct", KindOAuth); err != nil || got != v2 {
		t.Fatalf("published value lost: len=%d err=%v", len(got), err)
	}
}

// A pre-v2.0.10 binary does not take secrets.lock: a fresh pending generation
// may belong to one that is still writing it, so it must not be swept yet.
// An interrupted save used to hold its chunks for 30 minutes on the theory that
// a pre-v2.0.10 binary might still be writing them. No binary does that any
// more, and every writer holds secrets.lock, so the chunks are garbage the
// moment the process dies and the next write reclaims them (#64).
func TestInterruptedSaveIsReclaimedByTheNextWrite(t *testing.T) {
	// Given
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	if err := s.Set("acct", KindOAuth, strings.Repeat("1", 5000)); err != nil {
		t.Fatal(err)
	}
	killOnSet(kr, "acct/oauth")
	crash(t, func() error { return s.Set("acct", KindOAuth, strings.Repeat("2", 5000)) })

	// When: another write happens right away.
	if err := s.Set("other", KindAPIKey, "sk"); err != nil {
		t.Fatal(err)
	}

	// Then
	if n := chunkItems(kr, "acct/oauth"); n != 3 {
		t.Fatalf("the orphaned generation was not reclaimed: %d chunk items left, want 3", n)
	}
	// And the value still reads back: reclaiming the orphan must not touch the
	// generation that is live.
	got, err := s.Get("acct", KindOAuth)
	if err != nil {
		t.Fatalf("read back after a sweep: %v", err)
	}
	if got != strings.Repeat("1", 5000) {
		t.Fatalf("sweeping the orphan changed the value: %d bytes", len(got))
	}
}

func TestIndexWrittenAsPlainKeyArrayStillPrunes(t *testing.T) {
	// Given: an index file from before pending entries existed.
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	kr.m[Service+"\x00gone/apikey"] = "sk"
	if err := os.WriteFile(s.indexPath(), []byte(`["gone/apikey"]`), 0o600); err != nil {
		t.Fatal(err)
	}

	// When
	if err := s.Prune(nil); err != nil {
		t.Fatal(err)
	}

	// Then
	if len(kr.m) != 0 {
		t.Fatalf("legacy-indexed item survived prune: %d items", len(kr.m))
	}
}
