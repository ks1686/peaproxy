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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	Service           = "peaproxy"
	EncryptedFileName = "secrets.enc"
	KeyFileName       = "secret.key"
	IndexFileName     = "secrets.index"
	Magic             = "PPSECv1\n"
)

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
}

type keyringAPI interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

func itemKey(id string, kind Kind) string {
	return id + "/" + string(kind)
}

func parseItemKey(k string) (id string, kind Kind, ok bool) {
	id, rest, found := strings.Cut(k, "/")
	if !found || id == "" || rest == "" {
		return "", "", false
	}
	return id, Kind(rest), true
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
	if s.backend == BackendKeyring {
		return s.keyringSet(itemKey(id, kind), value)
	}
	return s.fileSet(itemKey(id, kind), value)
}

// Get reads a secret.
func (s *Store) Get(id string, kind Kind) (string, error) {
	if s == nil {
		return "", ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if s.backend == BackendKeyring {
		return s.keyringPrune(keep)
	}
	return s.filePrune(keep)
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
