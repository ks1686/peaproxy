package secretstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
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
	scope = configScope(configPath)
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

// configScope names the scope of the config file at path by the file itself
// rather than by the name it was reached through. A symlink in the same
// directory (config.yaml -> work.yaml) resolves to its target, and the name is
// the one on disk, so a differently cased path on a case-insensitive
// filesystem is the same scope. The store stays next to path either way; a
// symlink to another directory keeps the name it was given. A config that does
// not exist yet is named as given.
func configScope(path string) string {
	dir, name := filepath.Dir(path), filepath.Base(path)
	if target, err := filepath.EvalSymlinks(path); err == nil && sameDir(filepath.Dir(target), dir) {
		name = filepath.Base(target)
	}
	want, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		return name
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return name
	}
	for _, e := range entries {
		if !strings.EqualFold(e.Name(), name) {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && os.SameFile(info, want) {
			return e.Name()
		}
	}
	return name
}

func sameDir(a, b string) bool {
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
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
	if readOnly(err) {
		return s.scopeReadOnly(scope)
	}
	if err != nil {
		return err
	}
	defer unlock()
	legacy := &Store{backend: s.backend, dir: s.dir, kr: s.kr, now: s.now}
	err = s.scopeLocked(legacy, scope)
	if readOnly(err) {
		// A secrets.lock left by an earlier run opens even in a directory that
		// has since become read-only, so the first failed create is where that
		// shows. Nothing was written: the marker and the directory id are the
		// first files a scope creates.
		s.scope, s.krPrefix = "", ""
		return s.scopeReadOnly(scope)
	}
	return err
}

func (s *Store) scopeLocked(legacy *Store, scope string) error {
	s.scope = scope
	if s.backend == BackendKeyring {
		id, err := s.dirIDLocked()
		if err != nil {
			return err
		}
		s.krPrefix = id + "/" + scope + "/"
	}
	migrated, err := s.migrateLocked(legacy)
	if err != nil {
		return err
	}
	if s.backend == BackendKeyring {
		// Keyring migrate only copies the legacy keychain index. Accounts that
		// still live in secrets.enc (a prior file backend, or a Keychain Set
		// that fell back) have to be imported even after a finished migrate,
		// or they stay oauth-login-required forever.
		if err := s.importFileSecretsLocked(); err != nil {
			return err
		}
	}
	if migrated {
		return nil
	}
	return s.carryLocked(legacy)
}

func readOnly(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS)
}

// scopeReadOnly serves a directory PeaProxy cannot write to (reads are allowed
// to need no write access): the scope when it already exists, otherwise the
// legacy store it would have migrated from, read as it is.
func (s *Store) scopeReadOnly(scope string) error {
	scoped := &Store{dir: s.dir, scope: scope}
	if !scoped.scopeAlreadyMigrated() {
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
// An empty `[]` index is not treated as finished: persist can write one
// before any secret is copied, and skipping on the file's existence stranded
// the legacy token (macOS 3.3.1, Claude oauth-login-required despite a
// Keychain item still named account/oauth).
//
// The keyring branch copies the legacy keychain index. Leftover secrets.enc
// items (absent from that index) are imported by importFileSecretsLocked,
// including after this copy is marked finished: a 3.3.2 machine that migrated
// Claude from Keychain otherwise stranded Antigravity/OpenAI still in the blob.
func (s *Store) migrateLocked(legacy *Store) (bool, error) {
	marker := filepath.Join(s.dir, scopedName("secrets.migrating", s.scope))
	_, merr := os.Stat(marker)
	interrupted := merr == nil
	if !interrupted && s.scopeAlreadyMigrated() {
		// 3.3.1 finished a copy without this file. Persist it while carry or
		// scoped secrets still prove the copy ran, before a prune deletes both.
		return false, s.persistMigratedLocked()
	}
	var keys []string
	switch s.backend {
	case BackendKeyring:
		idx, err := legacy.loadIndex()
		if err != nil {
			return false, fmt.Errorf("secretstore: reading the legacy keychain index to migrate: %w", err)
		}
		for k := range idx.keys {
			keys = append(keys, k)
		}
	default:
		// The file backend copies the whole blob; one placeholder entry says
		// there is something to copy. A leftover 0-item enc is not.
		if fileBlobHasItems(legacy.dir, legacy.scope) {
			keys = []string{""}
		}
	}
	if len(keys) == 0 {
		if interrupted {
			return false, os.Remove(marker)
		}
		return false, nil
	}
	if err := fslock.WriteAtomic(marker, nil, 0o600); err != nil {
		return false, err
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
				return false, fmt.Errorf("secretstore: migrating %q from the legacy keychain items: %w", k, err)
			}
			if err := s.setLocked(k, v); err != nil {
				return false, fmt.Errorf("secretstore: migrating %q into this config's keychain items: %w", k, err)
			}
		}
	} else {
		// The blob is encrypted with the directory's key, which the scope
		// shares, so the bytes carry over as they are.
		b, err := os.ReadFile(legacy.encPath())
		if err != nil {
			return false, err
		}
		if err := fslock.WriteAtomic(s.encPath(), b, 0o600); err != nil {
			return false, err
		}
	}
	if err := s.startCarryLocked(legacy); err != nil {
		return false, err
	}
	if err := s.persistMigratedLocked(); err != nil {
		return false, err
	}
	return true, os.Remove(marker)
}

func (s *Store) migratedPath() string {
	return filepath.Join(s.dir, scopedName("secrets.migrated", s.scope))
}

func (s *Store) persistMigratedLocked() error {
	if fileExists(s.migratedPath()) {
		return nil
	}
	return fslock.WriteAtomic(s.migratedPath(), nil, 0o600)
}

// scopeAlreadyMigrated reports whether this scope has finished a legacy copy
// (or already holds secrets of its own). A `[]` index, a 0-byte blob, or a
// leftover enc that decrypts to 0 items is not enough: those files can exist
// without a copy having run, and treating them as finished hid Keychain
// secrets from openAuto.
func (s *Store) scopeAlreadyMigrated() bool {
	if fileExists(s.carryPath()) || fileExists(s.migratedPath()) {
		return true
	}
	return s.scopeHasSecrets()
}

func (s *Store) scopeHasSecrets() bool {
	if fileBlobHasItems(s.dir, s.scope) {
		return true
	}
	return keyringIndexHasKeys(s.indexPath())
}

func (s *Store) importedPath() string {
	return filepath.Join(s.dir, scopedName("secrets.imported", s.scope))
}

// importFileSecretsLocked copies secrets that still live only in a leftover
// secrets.enc / secrets.<scope>.enc into this keyring scope. Copy, not move:
// the blobs stay so another config can still migrate from them. Keys already
// considered are recorded so a prune is not undone from the leftover blob.
func (s *Store) importFileSecretsLocked() error {
	items, err := s.encItemsLocked()
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	seen, err := s.loadImported()
	if err != nil {
		return err
	}
	changed := false
	for k, v := range items {
		if seen[k] {
			continue
		}
		_, gerr := s.keyringGet(k)
		switch {
		case gerr == nil:
			// Already in the keychain; remember so a later prune does not
			// copy it back from the never-pruned leftover blob.
		case errors.Is(gerr, ErrNotFound), errors.Is(gerr, ErrUnreadable):
			if err := s.setLocked(k, v); err != nil {
				return fmt.Errorf("secretstore: importing %q from the encrypted file: %w", k, err)
			}
		default:
			return fmt.Errorf("secretstore: reading %q from the keychain while importing the encrypted file: %w", k, gerr)
		}
		seen[k] = true
		changed = true
	}
	if !changed {
		return nil
	}
	return s.saveImported(seen)
}

// encItemsLocked decrypts the legacy blob then this scope's blob. The scope
// wins on a shared key (a persist fallback writes there). An unreadable blob
// is skipped so a leftover corrupt secrets.enc cannot brick a keychain config.
func (s *Store) encItemsLocked() (map[string]string, error) {
	items := map[string]string{}
	scopes := []string{""}
	if s.scope != "" {
		scopes = append(scopes, s.scope)
	}
	for _, scope := range scopes {
		tmp := &Store{dir: s.dir, scope: scope, now: s.now}
		if !nonEmptyFile(tmp.encPath()) {
			continue
		}
		blob, err := tmp.loadBlob(false)
		if err != nil {
			log.Printf("peaproxy: could not read %s while importing secrets into the OS keychain: %v", tmp.encPath(), err)
			continue
		}
		for k, v := range blob.Items {
			if k == "" || v == "" {
				continue
			}
			items[k] = v
		}
	}
	return items, nil
}

func (s *Store) loadImported() (map[string]bool, error) {
	seen := map[string]bool{}
	b, err := os.ReadFile(s.importedPath())
	if err != nil {
		if os.IsNotExist(err) {
			return seen, nil
		}
		return nil, err
	}
	var keys []string
	if json.Unmarshal(b, &keys) != nil {
		return seen, nil
	}
	for _, k := range keys {
		if k != "" {
			seen[k] = true
		}
	}
	return seen, nil
}

func (s *Store) saveImported(seen map[string]bool) error {
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	return fslock.WriteAtomic(s.importedPath(), b, 0o600)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
