package secretstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileBackendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() != BackendFile {
		t.Fatalf("backend %s", s.Backend())
	}
	if err := s.Set("anthropic-oauth", KindOAuth, `{"accessToken":"at","refreshToken":"rt"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("anthropic-key", KindAPIKey, "sk-test"); err != nil {
		t.Fatal(err)
	}

	again, err := OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.Get("anthropic-oauth", KindOAuth)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"accessToken":"at"`) || !strings.Contains(got, `"refreshToken":"rt"`) {
		t.Fatalf("oauth %s", got)
	}
	key, err := again.Get("anthropic-key", KindAPIKey)
	if err != nil || key != "sk-test" {
		t.Fatalf("apikey %q %v", key, err)
	}
}

func TestFileBackendMissingIsNotFound(t *testing.T) {
	s, err := OpenFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Get("missing", KindAPIKey)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestFileBackendPruneRemovesDeletedAccounts(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("keep", KindAPIKey, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("drop", KindAPIKey, "b"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("drop", KindOAuth, "{}"); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune([]string{"keep"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("drop", KindAPIKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("drop apikey still present: %v", err)
	}
	if _, err := s.Get("drop", KindOAuth); !errors.Is(err, ErrNotFound) {
		t.Fatalf("drop oauth still present: %v", err)
	}
	got, err := s.Get("keep", KindAPIKey)
	if err != nil || got != "a" {
		t.Fatalf("keep %q %v", got, err)
	}
}

func TestFileBackendEncryptedNotPlaintextOnDisk(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	secret := "sk-super-secret-value"
	if err := s.Set("acct", KindAPIKey, secret); err != nil {
		t.Fatal(err)
	}
	enc, err := os.ReadFile(filepath.Join(dir, EncryptedFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), secret) {
		t.Fatal("secret stored in plaintext in secrets.enc")
	}
	if !strings.HasPrefix(string(enc), Magic) {
		t.Fatalf("missing magic prefix: %q", enc[:min(len(enc), 12)])
	}
}

func TestOpenUsesFileBackendDuringGoTest(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() != BackendFile {
		t.Fatalf("tests must not touch the OS keychain, got %s", s.Backend())
	}
}

func TestOpenHonorsFileEnv(t *testing.T) {
	t.Setenv("PEAPROXY_SECRET_BACKEND", "file")
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() != BackendFile {
		t.Fatalf("got %s", s.Backend())
	}
}

type memKeyring struct {
	m map[string]string
}

func (m *memKeyring) Set(service, user, password string) error {
	m.m[service+"\x00"+user] = password
	return nil
}

func (m *memKeyring) Get(service, user string) (string, error) {
	v, ok := m.m[service+"\x00"+user]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (m *memKeyring) Delete(service, user string) error {
	delete(m.m, service+"\x00"+user)
	return nil
}

func TestKeyringBackendRoundTripAndPrune(t *testing.T) {
	dir := t.TempDir()
	s := &Store{backend: BackendKeyring, dir: dir, kr: &memKeyring{m: map[string]string{}}}
	if err := s.Set("acct", KindOAuth, "tok"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("gone", KindAPIKey, "sk"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("acct", KindOAuth)
	if err != nil || got != "tok" {
		t.Fatalf("%q %v", got, err)
	}
	if err := s.Prune([]string{"acct"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("gone", KindAPIKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pruned key still present: %v", err)
	}
	if got, err = s.Get("acct", KindOAuth); err != nil || got != "tok" {
		t.Fatalf("kept %q %v", got, err)
	}
}

type cappedKeyring struct{ memKeyring }

func (c *cappedKeyring) Set(service, user, password string) error {
	if len(service)+len(user)+len(password) > 3000 {
		return errors.New("data passed to Set was too big")
	}
	return c.memKeyring.Set(service, user, password)
}

func TestKeyringBackendChunksOversizedSecrets(t *testing.T) {
	kr := &cappedKeyring{memKeyring{m: map[string]string{}}}
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	big := strings.Repeat("codex-id-token.", 700)
	if err := s.Set("openai-oauth", KindOAuth, big); err != nil {
		t.Fatalf("oversized secret not stored: %v", err)
	}
	if got, err := s.Get("openai-oauth", KindOAuth); err != nil || got != big {
		t.Fatalf("round trip len=%d err=%v", len(got), err)
	}
	if err := s.Set("openai-oauth", KindOAuth, "small"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get("openai-oauth", KindOAuth); err != nil || got != "small" {
		t.Fatalf("shrunk %q %v", got, err)
	}
	if len(kr.m) != 1 {
		t.Fatalf("stale chunks left behind: %d items", len(kr.m))
	}
	if err := s.Set("openai-oauth", KindOAuth, big); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(nil); err != nil {
		t.Fatal(err)
	}
	if len(kr.m) != 0 {
		t.Fatalf("prune left %d items", len(kr.m))
	}
}

func TestFileDecryptRejectsTamper(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("acct", KindAPIKey, "sk"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, EncryptedFileName)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-1] ^= 0xff
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("acct", KindAPIKey); err == nil {
		t.Fatal("expected decrypt error after tamper")
	}
}
