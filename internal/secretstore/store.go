// Package secretstore keeps OAuth tokens and API keys out of plaintext YAML.
//
// Default: OS keychain (macOS Keychain, Windows Credential Manager, Linux
// Secret Service) via zalando/go-keyring. If that is unavailable, an AES-256-GCM
// file next to the config (machine-local key, mode 0600).
//
// Tests and PEAPROXY_SECRET_BACKEND=file always use the encrypted file backend.
package secretstore

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/fslock"
)

const (
	Service           = "peaproxy"
	EncryptedFileName = "secrets.enc"
	KeyFileName       = "secret.key"
	IndexFileName     = "secrets.index"
	LockFileName      = "secrets.lock"
	Magic             = "PPSECv1\n"
)

// lockTimeout bounds the wait for another process's store operation (which
// may be blocked on a keychain prompt).
var lockTimeout = 15 * time.Second

// Kind is the secret type stored per account id.
type Kind string

const (
	KindOAuth  Kind = "oauth"
	KindAPIKey Kind = "apikey"
)

// Backend names the active storage.
type Backend string

const (
	BackendFile    Backend = "file"
	BackendKeyring Backend = "keyring"
)

// ErrNotFound means that account/kind has no stored secret.
var ErrNotFound = errors.New("secret not found")

// ErrUnreadable means a stored secret exists but cannot be reassembled
// (missing chunk, corrupt header). Callers may treat it as absent.
var ErrUnreadable = errors.New("secret unreadable")

// Store is a small secret map keyed by account id + kind.
type Store struct {
	mu      sync.Mutex
	backend Backend
	dir     string
	kr      keyringAPI
	now     func() time.Time // nil means time.Now; tests move it forward
	// scope is the config file this store belongs to ("" is the legacy
	// directory-wide store). It names the blob and index files.
	scope string
	// krPrefix namespaces keychain items: "<dir id>/<scope>/", "" for legacy.
	krPrefix string
}

type keyringAPI interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

func itemKey(id string, kind Kind) string {
	return id + "/" + string(kind)
}

// parseItemKey splits an item key back into its id and kind. The id may itself
// contain slashes -- "work/openai" is a perfectly good account id -- and a kind
// never does, so the split is on the LAST slash. Cutting at the first one, as
// this used to, reduced "work/openai" to the account "work", and Prune then
// deleted the secret it had just written (#77).
func parseItemKey(k string) (id string, kind Kind, ok bool) {
	cut := strings.LastIndex(k, "/")
	if cut <= 0 || cut == len(k)-1 {
		return "", "", false
	}
	return k[:cut], Kind(k[cut+1:]), true
}

func accountOf(k string) string {
	id, _, ok := parseItemKey(k)
	if !ok {
		return ""
	}
	return id
}

func backendFromEnv() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("PEAPROXY_SECRET_BACKEND")))
}

// Open picks OS keychain when it works, otherwise the encrypted file in dir.
// During `go test`, the file backend is forced so CI and developer keychains
// are never touched (override with PEAPROXY_SECRET_BACKEND=keyring).
func Open(dir string) (*Store, error) {
	if dir == "" {
		dir = "."
	}
	return openBackend(dir, "")
}

func openBackend(dir, scope string) (*Store, error) {
	switch backendFromEnv() {
	case "file":
		return OpenFile(dir)
	case "keyring":
		return openKeyring(dir)
	default:
		if testing.Testing() {
			return OpenFile(dir)
		}
		return openAuto(dir, scope)
	}
}

// openKeyringFn is openKeyring, replaceable in tests.
var openKeyringFn = openKeyring

// openAuto picks the backend that already holds this directory's secrets and
// only chooses afresh for a directory that has none. Re-probing on every Open
// let one failed keychain probe (a locked login keychain) write a rotated
// refresh token to secrets.enc while the keychain kept the stale one, which the
// next load then read back; the reverse switch hid file secrets once the
// keychain answered again.
//
// A config's own scoped store is the evidence when it names secrets, holds a
// blob, or has already migrated (carry / migrated marker). An empty `[]`
// index is not, unless that marker says the copy already ran. Before
// migration, the legacy directory-wide store it will migrate from is.
func openAuto(dir, scope string) (*Store, error) {
	index, enc := filepath.Join(dir, scopedName(IndexFileName, scope)), filepath.Join(dir, scopedName(EncryptedFileName, scope))
	if scope != "" && !(&Store{dir: dir, scope: scope}).scopeAlreadyMigrated() {
		index, enc = filepath.Join(dir, IndexFileName), filepath.Join(dir, EncryptedFileName)
	}
	switch {
	case keyringIndexHasKeys(index):
		s, err := openKeyringFn(dir)
		if err != nil {
			return nil, fmt.Errorf("secretstore: this config's secrets are in the OS keychain, which is unavailable: %w", err)
		}
		return s, nil
	case nonEmptyFile(enc):
		return OpenFile(dir)
	}
	if s, err := openKeyringFn(dir); err == nil {
		return s, nil
	}
	return OpenFile(dir)
}

// keyringIndexHasKeys reports whether this directory's keychain index names any
// secret. Prune writes an index even when the keychain holds nothing, so an
// empty one says nothing about where secrets live; an unreadable one is taken
// as holding keys, so a damaged index cannot divert writes to the file.
func keyringIndexHasKeys(path string) bool {
	if !nonEmptyFile(path) {
		return false
	}
	idx, err := loadIndexFile(path)
	return err != nil || len(idx.keys) > 0
}

func nonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// OpenFile always uses the AES-GCM file next to dir (CI / tests).
func OpenFile(dir string) (*Store, error) {
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{backend: BackendFile, dir: dir}, nil
}

func openKeyring(dir string) (*Store, error) {
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	kr := liveKeyring{}
	if err := probeKeyring(kr); err != nil {
		return nil, err
	}
	return &Store{backend: BackendKeyring, dir: dir, kr: kr}, nil
}

// Backend reports which storage is active.
func (s *Store) Backend() Backend {
	if s == nil {
		return BackendFile
	}
	return s.backend
}

// Dir is the config directory (file blob + keyring index).
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Set writes a secret.
func (s *Store) Set(id string, kind Kind, value string) error {
	if s == nil {
		return errors.New("secret store is nil")
	}
	if id == "" || kind == "" {
		return errors.New("secretstore: missing id or kind")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.setLocked(itemKey(id, kind), value)
}

// Get reads a secret. When secrets.lock cannot be opened for writing (a
// permission error or a read-only filesystem), it falls back to an unlocked
// read. Writers still require the lock.
func (s *Store) Get(id string, kind Kind) (string, error) {
	if s == nil {
		return "", ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	locked := err == nil
	switch {
	case locked:
		defer unlock()
	case !errors.Is(err, fs.ErrPermission) && !errors.Is(err, syscall.EROFS):
		return "", err
	}
	return s.getLockedAt(itemKey(id, kind), !locked)
}

// Delete removes one secret.
func (s *Store) Delete(id string, kind Kind) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.deleteLocked(itemKey(id, kind))
}

// Prune drops secrets whose account id is not in keepIDs.
func (s *Store) Prune(keepIDs []string) error {
	if s == nil {
		return nil
	}
	keep := make(map[string]struct{}, len(keepIDs))
	for _, id := range keepIDs {
		if id != "" {
			keep[id] = struct{}{}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.pruneLocked(keep)
}

func (s *Store) getLocked(k string) (string, error) {
	return s.getLockedAt(k, false)
}

func (s *Store) getLockedAt(k string, shared bool) (string, error) {
	if s.backend != BackendKeyring {
		// shared: on this path a key file may be caught mid-creation by a writer
		// holding the lock, so the key read has to tolerate one (#64).
		return s.fileGet(k, shared)
	}
	v, err := s.keyringGet(k)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return v, err
	}
	v, err = s.fileGetExisting(k)
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnreadable) {
		return v, err
	}
	return "", fmt.Errorf("%w: %v", ErrUnreadable, err)
}

// setLocked writes to the OS keychain and, if that fails for a secret the
// keychain does not already hold, to this scope's encrypted file so a
// LaunchAgent (or any keychain that refuses Set) still keeps a successful
// OAuth token. An overwrite the keychain refused is still an error: Get would
// keep returning the old item. Fallback runs only on ErrNotFound; any other
// Get error (ErrUnreadable, ACL) would hide the file copy.
func (s *Store) setLocked(k, v string) error {
	if s.backend != BackendKeyring {
		return s.fileSet(k, v)
	}
	err := s.keyringSet(k, v)
	if err == nil {
		return nil
	}
	if _, gerr := s.keyringGet(k); !errors.Is(gerr, ErrNotFound) {
		return err
	}
	if ferr := s.fileSet(k, v); ferr != nil {
		return fmt.Errorf("%w (also failed to save in the encrypted file: %v)", err, ferr)
	}
	log.Printf("peaproxy: OS keychain refused to store %q (%v); saved it in the encrypted file instead", k, err)
	return nil
}

func (s *Store) deleteLocked(k string) error {
	if s.backend != BackendKeyring {
		return s.fileDelete(k)
	}
	kerr := s.keyringDelete(k)
	_ = s.fileDeleteExisting(k)
	return kerr
}

func (s *Store) pruneLocked(keep map[string]struct{}) error {
	if s.backend != BackendKeyring {
		return s.filePrune(keep)
	}
	kerr := s.keyringPrune(keep)
	_ = s.filePruneExisting(keep)
	return kerr
}

// lock takes secrets.lock for one whole operation, so no two PeaProxy
// processes interleave a read-modify-write of the index, the blob or a
// chunked keychain item. Lock order: s.mu, then secrets.lock; config.lock,
// when held, is always taken before either.
func (s *Store) lock() (unlock func(), err error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	unlock, err = fslock.Lock(filepath.Join(s.dir, LockFileName), lockTimeout)
	if errors.Is(err, fslock.ErrBusy) {
		return nil, fmt.Errorf("secretstore: %s busy (another peaproxy process is saving): %w", s.dir, err)
	}
	return unlock, err
}

func (s *Store) keyPath() string {
	return filepath.Join(s.dir, KeyFileName)
}

func (s *Store) encPath() string {
	return filepath.Join(s.dir, scopedName(EncryptedFileName, s.scope))
}

func (s *Store) indexPath() string {
	return filepath.Join(s.dir, scopedName(IndexFileName, s.scope))
}
