package secretstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func scopedKeyring(t *testing.T, dir, config string, kr keyringAPI) *Store {
	t.Helper()
	s := &Store{backend: BackendKeyring, dir: dir, kr: kr}
	if err := s.scopeTo(config); err != nil {
		t.Fatal(err)
	}
	return s
}

func scopedFile(t *testing.T, dir, config string) *Store {
	t.Helper()
	s := &Store{backend: BackendFile, dir: dir}
	if err := s.scopeTo(config); err != nil {
		t.Fatal(err)
	}
	return s
}

// Keychain items were named by account id alone, so two config directories
// with an account called "anthropic" shared one item: a login in one replaced
// the other's token.
func TestTwoDirectoriesKeepSeparateKeychainItems(t *testing.T) {
	kr := &memKeyring{m: map[string]string{}}
	a := scopedKeyring(t, t.TempDir(), "config.yaml", kr)
	b := scopedKeyring(t, t.TempDir(), "config.yaml", kr)
	if err := a.Set("anthropic", KindOAuth, "token-a"); err != nil {
		t.Fatal(err)
	}
	if err := b.Set("anthropic", KindOAuth, "token-b"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Get("anthropic", KindOAuth); err != nil || got != "token-a" {
		t.Fatalf("dir a reads %q, %v; want its own token", got, err)
	}
	if err := b.Prune(nil); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Get("anthropic", KindOAuth); err != nil || got != "token-a" {
		t.Fatalf("pruning dir b removed dir a's token: %q, %v", got, err)
	}
}

// Two config files in one directory shared one store, so saving one pruned
// every secret the other held.
func TestTwoConfigsInOneDirectoryKeepSeparateSecrets(t *testing.T) {
	for _, backend := range []Backend{BackendFile, BackendKeyring} {
		t.Run(string(backend), func(t *testing.T) {
			dir := t.TempDir()
			kr := &memKeyring{m: map[string]string{}}
			open := func(config string) *Store {
				if backend == BackendFile {
					return scopedFile(t, dir, config)
				}
				return scopedKeyring(t, dir, config, kr)
			}
			a, b := open("a.yaml"), open("b.yaml")
			if err := a.Set("x", KindAPIKey, "sk-a"); err != nil {
				t.Fatal(err)
			}
			if err := b.Set("y", KindAPIKey, "sk-b"); err != nil {
				t.Fatal(err)
			}
			if err := b.Prune([]string{"y"}); err != nil {
				t.Fatal(err)
			}
			if got, err := open("a.yaml").Get("x", KindAPIKey); err != nil || got != "sk-a" {
				t.Fatalf("saving b.yaml pruned a.yaml's secret: %q, %v", got, err)
			}
		})
	}
}

func TestExistingSecretsMigrateIntoTheConfigsScope(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		dir := t.TempDir()
		legacy, err := OpenFile(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := legacy.Set("acct", KindOAuth, "tok"); err != nil {
			t.Fatal(err)
		}
		s := scopedFile(t, dir, "config.yaml")
		if got, err := s.Get("acct", KindOAuth); err != nil || got != "tok" {
			t.Fatalf("legacy secret after migration: %q, %v", got, err)
		}
		// A second config in the directory gets its own copy, untouched by the
		// first one's prune.
		if err := s.Prune(nil); err != nil {
			t.Fatal(err)
		}
		if got, err := scopedFile(t, dir, "other.yaml").Get("acct", KindOAuth); err != nil || got != "tok" {
			t.Fatalf("second config's migration after the first pruned: %q, %v", got, err)
		}
	})
	t.Run("keyring", func(t *testing.T) {
		dir := t.TempDir()
		kr := &memKeyring{m: map[string]string{}}
		legacy := &Store{backend: BackendKeyring, dir: dir, kr: kr}
		if err := legacy.Set("acct", KindOAuth, "tok"); err != nil {
			t.Fatal(err)
		}
		s := scopedKeyring(t, dir, "config.yaml", kr)
		if got, err := s.Get("acct", KindOAuth); err != nil || got != "tok" {
			t.Fatalf("legacy keychain secret after migration: %q, %v", got, err)
		}
		if err := s.Prune(nil); err != nil {
			t.Fatal(err)
		}
		if got, err := legacy.Get("acct", KindOAuth); err != nil || got != "tok" {
			t.Fatalf("the scoped prune touched the legacy item: %q, %v", got, err)
		}
	})
}

// persistTo prunes on every successful save, so a 3.3.1 config that loaded
// without copying the legacy Keychain token wrote secrets.<config>.index as
// `[]`. That file used to make migrateLocked return without copying, leaving
// Claude oauth-login-required while the old account/oauth item still existed.
func TestAnEmptyScopedIndexDoesNotBlockLegacyKeyringMigration(t *testing.T) {
	dir := t.TempDir()
	kr := &memKeyring{m: map[string]string{}}
	legacy := &Store{backend: BackendKeyring, dir: dir, kr: kr}
	if err := legacy.Set("anthropic-oauth", KindOAuth, "legacy-tok"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.config.yaml.index"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := scopedKeyring(t, dir, "config.yaml", kr)
	if got, err := s.Get("anthropic-oauth", KindOAuth); err != nil || got != "legacy-tok" {
		t.Fatalf("scoped store after empty-index open: %q, %v", got, err)
	}
	if got, err := legacy.Get("anthropic-oauth", KindOAuth); err != nil || got != "legacy-tok" {
		t.Fatalf("legacy item was rewritten: %q, %v", got, err)
	}
}

// An empty `[]` scoped index is the same migrate gate as a missing one: both
// copy the legacy store unless a finished-migrate marker (carry or migrated)
// or real scoped secrets say the copy already ran.
func TestAnEmptyScopedIndexMigratesLikeAMissingIndex(t *testing.T) {
	eachBackend(t, func(t *testing.T, backend Backend) {
		open := func(t *testing.T, withEmptyIndex bool) *Store {
			t.Helper()
			dir := t.TempDir()
			kr := &memKeyring{m: map[string]string{}}
			legacy := &Store{backend: backend, dir: dir, kr: kr}
			if err := legacy.Set("acct", KindOAuth, "tok"); err != nil {
				t.Fatal(err)
			}
			if withEmptyIndex {
				if err := os.WriteFile(filepath.Join(dir, "secrets.config.yaml.index"), []byte("[]"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			s := &Store{backend: backend, dir: dir, kr: kr}
			if err := s.scopeTo("config.yaml"); err != nil {
				t.Fatal(err)
			}
			return s
		}
		missing := open(t, false)
		empty := open(t, true)
		gotMissing, errMissing := missing.Get("acct", KindOAuth)
		gotEmpty, errEmpty := empty.Get("acct", KindOAuth)
		if errMissing != nil || gotMissing != "tok" {
			t.Fatalf("missing index: %q, %v", gotMissing, errMissing)
		}
		if errEmpty != nil || gotEmpty != "tok" {
			t.Fatalf("empty index: %q, %v", gotEmpty, errEmpty)
		}
	})
}

func TestPruneOfAnEmptyKeyringScopeDoesNotCreateAnIndex(t *testing.T) {
	dir := t.TempDir()
	s := &Store{backend: BackendKeyring, dir: dir, kr: &memKeyring{m: map[string]string{}}}
	if err := s.scopeTo("config.yaml"); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune([]string{"anthropic-oauth"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets.config.yaml.index")); !os.IsNotExist(err) {
		t.Fatalf("prune created an empty index: %v", err)
	}
}

// A keychain write refused mid-migrate still keeps the remaining secrets
// (file fallback) instead of leaving the scope with half the tokens.
func TestAnInterruptedMigrationRunsAgain(t *testing.T) {
	dir := t.TempDir()
	kr := &memKeyring{m: map[string]string{}}
	legacy := &Store{backend: BackendKeyring, dir: dir, kr: kr}
	for _, id := range []string{"one", "two"} {
		if err := legacy.Set(id, KindAPIKey, "sk-"+id); err != nil {
			t.Fatal(err)
		}
	}
	failing := &failAfterKeyring{memKeyring: kr, left: 1}
	s := &Store{backend: BackendKeyring, dir: dir, kr: failing}
	if err := s.scopeTo("config.yaml"); err != nil {
		t.Fatalf("migrate with a refused keychain write: %v", err)
	}
	for _, id := range []string{"one", "two"} {
		if got, err := s.Get(id, KindAPIKey); err != nil || got != "sk-"+id {
			t.Fatalf("%s after a refused keychain write: %q, %v", id, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets.config.yaml.migrating")); !os.IsNotExist(err) {
		t.Fatalf("migration marker left behind: %v", err)
	}
}

// failAfterKeyring lets `left` scoped writes through, then fails.
type failAfterKeyring struct {
	*memKeyring
	left int
}

func (f *failAfterKeyring) Set(service, user, password string) error {
	if strings.Contains(user, "config.yaml/") {
		if f.left <= 0 {
			return errors.New("keychain write refused")
		}
		f.left--
	}
	return f.memKeyring.Set(service, user, password)
}

// One config file reached by two names must have one scope: a symlinked
// profile (config.yaml -> work.yaml) or, on a case-insensitive filesystem, a
// differently cased path. Otherwise a login saved under one name is invisible
// to a server reading the other.
func TestOneConfigFileHasOneScopeWhateverItIsCalled(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work.yaml")
	if err := os.WriteFile(work, []byte("schemaVersion: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("work.yaml", filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if got := configScope(filepath.Join(dir, "config.yaml")); got != "work.yaml" {
		t.Fatalf("scope via the symlink = %q, want work.yaml", got)
	}
	if got := configScope(work); got != "work.yaml" {
		t.Fatalf("scope via the file = %q, want work.yaml", got)
	}
	upper := filepath.Join(dir, "WORK.YAML")
	if _, err := os.Stat(upper); err == nil {
		if got := configScope(upper); got != "work.yaml" {
			t.Fatalf("scope via a differently cased path = %q, want the on-disk name", got)
		}
	}
	if got := configScope(filepath.Join(dir, "fresh.yaml")); got != "fresh.yaml" {
		t.Fatalf("scope of a config that does not exist yet = %q", got)
	}
}
