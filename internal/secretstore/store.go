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
	switch backendFromEnv() {
	case "file":
		return OpenFile(dir)
	case "keyring":
		return openKeyring(dir)
	default:
		if testing.Testing() {
			return OpenFile(dir)
		}
		s, err := openKeyring(dir)
		if err == nil {
			return s, nil
		}
		return OpenFile(dir)
	}
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
	if s.backend == BackendKeyring {
		return s.keyringSet(itemKey(id, kind), value)
	}
	return s.fileSet(itemKey(id, kind), value)
}

// Get reads a secret. When secrets.lock cannot be opened for writing (a
// permission error or a read-only filesystem), it falls back to the unlocked
// read of releases before v2.0.10. Writers still require the lock.
func (s *Store) Get(id string, kind Kind) (string, error) {
	if s == nil {
		return "", ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	switch {
	case err == nil:
		defer unlock()
	case !errors.Is(err, fs.ErrPermission) && !errors.Is(err, syscall.EROFS):
		return "", err
	}
	if s.backend == BackendKeyring {
		return s.keyringGet(itemKey(id, kind))
	}
	return s.fileGet(itemKey(id, kind))
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
	if s.backend == BackendKeyring {
		return s.keyringDelete(itemKey(id, kind))
	}
	return s.fileDelete(itemKey(id, kind))
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
	if s.backend == BackendKeyring {
		return s.keyringPrune(keep)
	}
	return s.filePrune(keep)
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
	return filepath.Join(s.dir, EncryptedFileName)
}

func (s *Store) indexPath() string {
	return filepath.Join(s.dir, IndexFileName)
}
