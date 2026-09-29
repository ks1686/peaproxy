package secretstore

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/zalando/go-keyring"
)

// faultKeyring behaves like the live darwin keyring for NotFound (keyring.ErrNotFound)
// and lets a test fail one user's Set/Get or run a hook before each Set.
type faultKeyring struct {
	m       map[string]string
	failSet string
	failGet string
	getErr  error
	onSet   func(user string) (consumed bool)
	onGet   func(user string) (consumed bool)
}

func newFault() *faultKeyring { return &faultKeyring{m: map[string]string{}} }

// isItem reports whether user is want. A chunk name "<key>#<i>" also matches
// the generation-scoped name "<key>#<gen>.<i>" for the same chunk index.
func isItem(user, want string) bool {
	if user == want {
		return true
	}
	base, idx, ok := strings.Cut(want, "#")
	if !ok {
		return false
	}
	gen, ok := strings.CutPrefix(user, base+"#")
	return ok && strings.HasSuffix(gen, "."+idx) && !strings.Contains(strings.TrimSuffix(gen, "."+idx), ".")
}

func (f *faultKeyring) Set(service, user, password string) error {
	if f.onSet != nil {
		hook := f.onSet
		f.onSet = nil
		if !hook(user) {
			f.onSet = hook
		}
	}
	if f.failSet != "" && isItem(user, f.failSet) {
		return errors.New("security: user interaction is not allowed")
	}
	f.m[service+"\x00"+user] = password
	return nil
}

func (f *faultKeyring) Get(service, user string) (string, error) {
	if f.onGet != nil {
		hook := f.onGet
		f.onGet = nil
		if !hook(user) {
			f.onGet = hook
		}
	}
	if user == f.failGet && f.getErr != nil {
		err := f.getErr
		f.getErr = nil
		return "", err
	}
	v, ok := f.m[service+"\x00"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (f *faultKeyring) Delete(service, user string) error {
	if _, ok := f.m[service+"\x00"+user]; !ok {
		return keyring.ErrNotFound
	}
	delete(f.m, service+"\x00"+user)
	return nil
}

func TestOverwriteKeepsOldValueWhenAChunkWriteFails(t *testing.T) {
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	v1 := strings.Repeat("1", 5000)
	v2 := strings.Repeat("2", 5000)
	if err := s.Set("acct", KindOAuth, v1); err != nil {
		t.Fatal(err)
	}
	kr.failSet = "acct/oauth#1"
	if err := s.Set("acct", KindOAuth, v2); err == nil {
		t.Fatal("expected write failure")
	}
	kr.failSet = ""
	got, err := s.Get("acct", KindOAuth)
	if err != nil {
		t.Fatalf("get after failed overwrite: %v", err)
	}
	if got != v1 && got != v2 {
		t.Fatalf("torn value: %d leading '2' bytes then %d '1' bytes", strings.Count(got, "2"), strings.Count(got, "1"))
	}
}

// The two writers below interleave inside one Set, which the per-dir lock
// forbids in-process. Distinct dirs model what the lock cannot cover (another
// process sharing the keychain); generation-scoped chunks must still hold.
func TestConcurrentWritersNeverSplice(t *testing.T) {
	kr := newFault()
	a := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	b := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	v0 := strings.Repeat("0", 5000)
	v1 := strings.Repeat("1", 5000)
	v2 := strings.Repeat("2", 3000)
	if err := a.Set("acct", KindOAuth, v0); err != nil {
		t.Fatal(err)
	}
	kr.onSet = func(user string) bool {
		if !isItem(user, "acct/oauth#1") {
			return false
		}
		if err := b.Set("acct", KindOAuth, v2); err != nil {
			t.Errorf("writer b: %v", err)
		}
		return true
	}
	if err := a.Set("acct", KindOAuth, v1); err != nil {
		t.Fatal(err)
	}
	got, err := a.Get("acct", KindOAuth)
	if err != nil {
		t.Fatalf("get after interleaved writes: %v", err)
	}
	if got != v1 && got != v2 {
		t.Fatalf("spliced value: %d '2' bytes + %d '1' bytes", strings.Count(got, "2"), strings.Count(got, "1"))
	}
}

func TestConcurrentWritersNeverLeaveDanglingHeader(t *testing.T) {
	kr := newFault()
	a := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	b := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	v0 := strings.Repeat("0", 5000)
	v1 := strings.Repeat("1", 5000)
	v2 := strings.Repeat("2", 3000)
	if err := a.Set("acct", KindOAuth, v0); err != nil {
		t.Fatal(err)
	}
	kr.onSet = func(user string) bool {
		if user != "acct/oauth" {
			return false
		}
		if err := b.Set("acct", KindOAuth, v2); err != nil {
			t.Errorf("writer b: %v", err)
		}
		return true
	}
	if err := a.Set("acct", KindOAuth, v1); err != nil {
		t.Fatal(err)
	}
	got, err := a.Get("acct", KindOAuth)
	if err != nil {
		t.Fatalf("get after interleaved writes: %v (isNotFound=%v)", err, errors.Is(err, ErrNotFound))
	}
	if got != v1 && got != v2 {
		t.Fatalf("spliced value len=%d", len(got))
	}
}

func TestDeleteRemovesChunksEvenIfHeaderReadFails(t *testing.T) {
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	if err := s.Set("acct", KindOAuth, strings.Repeat("x", 5000)); err != nil {
		t.Fatal(err)
	}
	kr.failGet = "acct/oauth"
	kr.getErr = errors.New("security: SecKeychainSearchCopyNext: interaction not allowed")
	err := s.Delete("acct", KindOAuth)
	if err == nil && len(kr.m) != 0 {
		t.Fatalf("delete reported success but left %d keychain items; index now %v", len(kr.m), mustIndex(t, s))
	}
}

func TestFailedFirstWriteLeavesNoChunks(t *testing.T) {
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	kr.failSet = "acct/oauth#1"
	if err := s.Set("acct", KindOAuth, strings.Repeat("x", 5000)); err == nil {
		t.Fatal("expected failure")
	}
	if len(kr.m) != 0 {
		t.Fatalf("failed first write left %d orphan items (not indexed, never pruned)", len(kr.m))
	}
}

func TestRawValueWithHeaderPrefixRoundTrips(t *testing.T) {
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: newFault()}
	v := chunkHeader + "2"
	if err := s.Set("acct", KindAPIKey, v); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("acct", KindAPIKey)
	if err != nil || got != v {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestChunksAreValidUTF8(t *testing.T) {
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	v := `{"email":"` + strings.Repeat("a", chunkSize-11) + `é","x":"` + strings.Repeat("b", 3000) + `"}`
	if !utf8.ValidString(v) {
		t.Fatal("fixture must be valid UTF-8")
	}
	if err := s.Set("acct", KindOAuth, v); err != nil {
		t.Fatal(err)
	}
	for k, item := range kr.m {
		if !utf8.ValidString(item) {
			t.Fatalf("item %q is not valid UTF-8 (ends % x)", k, []byte(item[len(item)-2:]))
		}
	}
	if got, err := s.Get("acct", KindOAuth); err != nil || got != v {
		t.Fatalf("round trip len=%d err=%v", len(got), err)
	}
}

func TestMissingChunkIsUnreadableNotAbsent(t *testing.T) {
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	if err := s.Set("acct", KindOAuth, strings.Repeat("x", 5000)); err != nil {
		t.Fatal(err)
	}
	for k := range kr.m {
		if strings.HasSuffix(k, ".1") {
			delete(kr.m, k)
		}
	}
	_, err := s.Get("acct", KindOAuth)
	if !errors.Is(err, ErrUnreadable) || errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrUnreadable only, got %v", err)
	}
}

func TestLegacyChunkLayoutReadsAndIsCleanedOnOverwrite(t *testing.T) {
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	kr.m[Service+"\x00acct/oauth"] = chunkHeader + "2"
	kr.m[Service+"\x00acct/oauth#0"] = "ab"
	kr.m[Service+"\x00acct/oauth#1"] = "cd"
	if got, err := s.Get("acct", KindOAuth); err != nil || got != "abcd" {
		t.Fatalf("legacy read %q %v", got, err)
	}
	if err := s.Set("acct", KindOAuth, "small"); err != nil {
		t.Fatal(err)
	}
	if len(kr.m) != 1 {
		t.Fatalf("legacy chunks left behind: %v", kr.m)
	}
}

type lockedKeyring struct {
	mu sync.Mutex
	faultKeyring
}

func (l *lockedKeyring) Set(service, user, password string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.faultKeyring.Set(service, user, password)
}

func (l *lockedKeyring) Get(service, user string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.faultKeyring.Get(service, user)
}

func (l *lockedKeyring) Delete(service, user string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.faultKeyring.Delete(service, user)
}

// config.Save opens a fresh Store per call, so Store.mu alone serializes
// nothing: same-dir Stores must share one lock for chunks and the index.
func TestSameDirStoresSerializeChunksAndIndex(t *testing.T) {
	kr := &lockedKeyring{faultKeyring: *newFault()}
	dir := t.TempDir()
	const writers = 8
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := &Store{backend: BackendKeyring, dir: dir, kr: kr}
			for j := 0; j < 5; j++ {
				if err := s.Set("acct"+strconv.Itoa(i), KindOAuth, strings.Repeat(strconv.Itoa(j), 5000)); err != nil {
					t.Error(err)
					return
				}
				if err := s.Set("shared", KindOAuth, strings.Repeat(strconv.Itoa(i), 4500)); err != nil {
					t.Error(err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	s := &Store{backend: BackendKeyring, dir: dir, kr: kr}
	idx := mustIndex(t, s)
	for i := 0; i < writers; i++ {
		if _, ok := idx[itemKey("acct"+strconv.Itoa(i), KindOAuth)]; !ok {
			t.Fatalf("index lost acct%d: %v", i, idx)
		}
	}
	got, err := s.Get("shared", KindOAuth)
	if err != nil || len(got) != 4500 || strings.Count(got, got[:1]) != 4500 {
		t.Fatalf("shared value spliced or unreadable: len=%d err=%v", len(got), err)
	}
	// Each 5000-byte value is a header + 3 chunks; the 4500-byte one also 3.
	if want := writers*4 + 4; len(kr.m) != want {
		t.Fatalf("keychain holds %d items, want %d (orphaned chunks)", len(kr.m), want)
	}
}

func TestReaderFollowsGenerationReplacedByAnotherProcess(t *testing.T) {
	kr := newFault()
	reader := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	otherProcess := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	v1 := strings.Repeat("1", 5000)
	v2 := strings.Repeat("2", 5000)
	if err := reader.Set("acct", KindOAuth, v1); err != nil {
		t.Fatal(err)
	}
	kr.onGet = func(user string) bool {
		if user == "acct/oauth" || !isItem(user, "acct/oauth#0") {
			return false
		}
		if err := otherProcess.Set("acct", KindOAuth, v2); err != nil {
			t.Fatal(err)
		}
		return true
	}
	got, err := reader.Get("acct", KindOAuth)
	if err != nil {
		t.Fatalf("reader lost a credential another process just rewrote: %v", err)
	}
	if got != v2 {
		t.Fatalf("got %d bytes starting %q, want the replacement value", len(got), got[:1])
	}
}

func TestSetAndDeleteRecoverFromCorruptHeader(t *testing.T) {
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	kr.m[Service+"\x00acct/oauth"] = chunkHeader + "zz:x"
	if err := s.Set("acct", KindOAuth, "fresh-login"); err != nil {
		t.Fatalf("re-login could not replace a corrupt header: %v", err)
	}
	if got, err := s.Get("acct", KindOAuth); err != nil || got != "fresh-login" {
		t.Fatalf("got %q %v", got, err)
	}
	kr.m[Service+"\x00acct/apikey"] = chunkHeader + "zz:x"
	if err := s.Delete("acct", KindAPIKey); err != nil {
		t.Fatalf("delete of a corrupt header failed: %v", err)
	}
	if _, ok := kr.m[Service+"\x00acct/apikey"]; ok {
		t.Fatal("corrupt header survived delete")
	}
}

func mustIndex(t *testing.T, s *Store) map[string]struct{} {
	t.Helper()
	idx, err := s.loadIndex()
	if err != nil {
		t.Fatal(err)
	}
	return idx
}
