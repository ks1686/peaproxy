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

// A migration cut short (a crash, a keychain prompt dismissed) runs again on
// the next open instead of leaving the scope with half the secrets.
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
	if err := s.scopeTo("config.yaml"); err == nil {
		t.Fatal("a failed migration write was not reported")
	}
	s = scopedKeyring(t, dir, "config.yaml", kr)
	for _, id := range []string{"one", "two"} {
		if got, err := s.Get(id, KindAPIKey); err != nil || got != "sk-"+id {
			t.Fatalf("%s after the rerun: %q, %v", id, got, err)
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
