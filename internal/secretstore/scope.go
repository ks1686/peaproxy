package secretstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ks1686/peaproxy/internal/fslock"
)

// DirIDFileName holds the random id that names this directory's keychain
// items, so two config directories never share one.
const DirIDFileName = "secrets.id"

// OpenFor opens the secret store of one config file.
//
// The store used to be per directory, with keychain items named by account id
// alone. A second config file in the directory pruned every secret the first
// one held whenever it saved, and two directories with an account of the same
// name shared one keychain item. Each config file now has its own scope: its own
// blob and index next to it, and keychain items under "<dir id>/<file name>/".
//
// The first open of a scope migrates the legacy directory-wide store into it
// (see migrateLocked).
func OpenFor(configPath string) (*Store, error) {
	dir, scope := filepath.Dir(configPath), filepath.Base(configPath)
	if configPath == "" || scope == "." || scope == string(filepath.Separator) {
		return Open(dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s, err := openBackend(dir, scope)
	if err != nil {
		return nil, err
	}
	if err := s.scopeTo(scope); err != nil {
		return nil, err
	}
	return s, nil
}

// scopedName is the legacy file name with the scope spliced in before its
// extension: secrets.enc becomes secrets.<scope>.enc.
func scopedName(name, scope string) string {
	if scope == "" {
		return name
	}
	ext := filepath.Ext(name)
	return strings.TrimSuffix(name, ext) + "." + scope + ext
}

func (s *Store) scopeTo(scope string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS) {
		return s.scopeReadOnly(scope)
	}
	if err != nil {
		return err
	}
	defer unlock()
	legacy := &Store{backend: s.backend, dir: s.dir, kr: s.kr, now: s.now}
	s.scope = scope
	if s.backend == BackendKeyring {
		id, err := s.dirIDLocked()
		if err != nil {
			return err
		}
		s.krPrefix = id + "/" + scope + "/"
	}
	return s.migrateLocked(legacy)
}

// scopeReadOnly serves a directory PeaProxy cannot write to (reads are allowed
// to need no write access): the scope when it already exists, otherwise the
// legacy store it would have migrated from, read as it is.
func (s *Store) scopeReadOnly(scope string) error {
	scoped := &Store{dir: s.dir, scope: scope}
	if !fileExists(scoped.indexPath()) && !fileExists(scoped.encPath()) {
		return nil
	}
	s.scope = scope
	if s.backend == BackendKeyring {
		b, err := os.ReadFile(filepath.Join(s.dir, DirIDFileName))
		if err != nil {
			return err
		}
		s.krPrefix = strings.TrimSpace(string(b)) + "/" + scope + "/"
	}
	return nil
}

func (s *Store) dirIDLocked() (string, error) {
	path := filepath.Join(s.dir, DirIDFileName)
	b, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(b))
		if _, herr := hex.DecodeString(id); herr != nil || len(id) != 32 {
			return "", fmt.Errorf("secretstore: %s is not a directory id; its keychain items cannot be named", path)
		}
		return id, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	if err := fslock.WriteAtomic(path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// migrateLocked copies the legacy directory-wide store into this scope the
// first time the scope is opened.
//
// The legacy store is copied, never moved, and is not written or pruned again:
// another config in the same directory, or another directory whose keychain
// items shared the old account-id names, may still have to migrate from it.
// A marker file makes a migration that was cut short run again in full.
func (s *Store) migrateLocked(legacy *Store) error {
	marker := filepath.Join(s.dir, scopedName("secrets.migrating", s.scope))
	_, merr := os.Stat(marker)
	interrupted := merr == nil
	if !interrupted && (fileExists(s.indexPath()) || fileExists(s.encPath())) {
		return nil
	}
	var keys []string
	switch s.backend {
	case BackendKeyring:
		idx, err := legacy.loadIndex()
		if err != nil {
			return fmt.Errorf("secretstore: reading the legacy keychain index to migrate: %w", err)
		}
		for k := range idx.keys {
			keys = append(keys, k)
		}
	default:
		// The file backend copies the whole blob; one placeholder entry says
		// there is something to copy.
		if nonEmptyFile(legacy.encPath()) {
			keys = []string{""}
		}
	}
	if len(keys) == 0 {
		if interrupted {
			return os.Remove(marker)
		}
		return nil
	}
	if err := fslock.WriteAtomic(marker, nil, 0o600); err != nil {
		return err
	}
	if s.backend == BackendKeyring {
		for _, k := range keys {
			v, err := legacy.keyringGet(k)
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnreadable) {
				// Nothing usable to carry over; the account logs in again, as
				// it would have with the legacy store.
				continue
			}
			if err != nil {
				return fmt.Errorf("secretstore: migrating %q from the legacy keychain items: %w", k, err)
			}
			if err := s.keyringSet(k, v); err != nil {
				return fmt.Errorf("secretstore: migrating %q into this config's keychain items: %w", k, err)
			}
		}
	} else {
		// The blob is encrypted with the directory's key, which the scope
		// shares, so the bytes carry over as they are.
		b, err := os.ReadFile(legacy.encPath())
		if err != nil {
			return err
		}
		if err := fslock.WriteAtomic(s.encPath(), b, 0o600); err != nil {
			return err
		}
	}
	return os.Remove(marker)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
