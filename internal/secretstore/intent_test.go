package secretstore

import (
	"os"
	"strings"
	"testing"
	"time"
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

func dayLater() time.Time { return time.Now().Add(24 * time.Hour) }

func TestInterruptedSaveOrphansAreSweptOnNextWrite(t *testing.T) {
	// Given: v2's chunks are written but the process dies before its header.
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	v1 := strings.Repeat("1", 5000)
	if err := s.Set("acct", KindOAuth, v1); err != nil {
		t.Fatal(err)
	}
	killOnSet(kr, "acct/oauth")
	crash(t, func() error { return s.Set("acct", KindOAuth, strings.Repeat("2", 5000)) })
	if n := chunkItems(kr, "acct/oauth"); n != 6 {
		t.Fatalf("want 3 live + 3 orphaned chunks after the crash, got %d", n)
	}

	// When: a later write lands in the same dir.
	s.now = dayLater
	if err := s.Set("other", KindAPIKey, "sk"); err != nil {
		t.Fatal(err)
	}

	// Then: the orphaned generation is gone and v1 still reads back.
	if n := chunkItems(kr, "acct/oauth"); n != 3 {
		t.Fatalf("%d orphaned chunks survived the next write", n-3)
	}
	if got, err := s.Get("acct", KindOAuth); err != nil || got != v1 {
		t.Fatalf("live value lost: len=%d err=%v", len(got), err)
	}
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

	// When
	s.now = dayLater
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

// The dir lock is in-process only: a fresh pending generation may belong to
// another process that is still writing it, so it must not be swept yet.
func TestSweepSparesGenerationsYoungEnoughToBeInFlight(t *testing.T) {
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
	if n := chunkItems(kr, "acct/oauth"); n != 6 {
		t.Fatalf("an in-flight generation was swept: %d chunk items left, want 6", n)
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
