package secretstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// carryStores returns a legacy store holding acct=v1 and an opener for the
// config's scope over the same directory and keychain.
func carryStores(t *testing.T, backend Backend) (*Store, func() *Store) {
	t.Helper()
	dir := t.TempDir()
	kr := &memKeyring{m: map[string]string{}}
	legacy := &Store{backend: backend, dir: dir, kr: kr}
	if err := legacy.Set("acct", KindOAuth, "v1"); err != nil {
		t.Fatal(err)
	}
	return legacy, func() *Store {
		s := &Store{backend: backend, dir: dir, kr: kr}
		if err := s.scopeTo("config.yaml"); err != nil {
			t.Fatal(err)
		}
		return s
	}
}

func eachBackend(t *testing.T, f func(t *testing.T, backend Backend)) {
	for _, b := range []Backend{BackendFile, BackendKeyring} {
		t.Run(string(b), func(t *testing.T) { f(t, b) })
	}
}

// After `brew upgrade`, the old server keeps running until it is restarted. Any
// new command in that window migrates, and the old server goes on refreshing
// tokens into the legacy store. Its newer token has to reach the scope, or the
// new server starts on the refresh token the old one already spent.
func TestATokenTheOldServerRefreshesAfterMigrationIsCarriedOver(t *testing.T) {
	eachBackend(t, func(t *testing.T, backend Backend) {
		legacy, open := carryStores(t, backend)
		open()
		if err := legacy.Set("acct", KindOAuth, "v2"); err != nil {
			t.Fatal(err)
		}
		if got, err := open().Get("acct", KindOAuth); err != nil || got != "v2" {
			t.Fatalf("scope reads %q, %v; want the old server's newer token", got, err)
		}
	})
}

func TestTheScopesOwnWritesWinOverTheLegacyStore(t *testing.T) {
	eachBackend(t, func(t *testing.T, backend Backend) {
		legacy, open := carryStores(t, backend)
		if err := open().Set("acct", KindOAuth, "mine"); err != nil {
			t.Fatal(err)
		}
		if err := legacy.Set("acct", KindOAuth, "theirs"); err != nil {
			t.Fatal(err)
		}
		if got, err := open().Get("acct", KindOAuth); err != nil || got != "mine" {
			t.Fatalf("scope reads %q, %v; the scope's own write must win", got, err)
		}
	})
}

func TestAnAccountTheOldBinaryAddsAfterMigrationIsCarriedOver(t *testing.T) {
	eachBackend(t, func(t *testing.T, backend Backend) {
		legacy, open := carryStores(t, backend)
		open()
		if err := legacy.Set("late", KindAPIKey, "sk-late"); err != nil {
			t.Fatal(err)
		}
		if got, err := open().Get("late", KindAPIKey); err != nil || got != "sk-late" {
			t.Fatalf("scope reads %q, %v; want the account logged in by the old binary", got, err)
		}
	})
}

// The legacy store is never pruned, so a secret the scope pruned is still
// there. It must stay gone from the scope.
func TestAPrunedSecretIsNotResurrectedFromTheLegacyStore(t *testing.T) {
	eachBackend(t, func(t *testing.T, backend Backend) {
		_, open := carryStores(t, backend)
		if err := open().Prune(nil); err != nil {
			t.Fatal(err)
		}
		if _, err := open().Get("acct", KindOAuth); !errors.Is(err, ErrNotFound) {
			t.Fatalf("pruned secret came back: %v", err)
		}
	})
}

func TestTheCarryWindowCloses(t *testing.T) {
	eachBackend(t, func(t *testing.T, backend Backend) {
		legacy, open := carryStores(t, backend)
		s := open()
		marker := filepath.Join(s.dir, "secrets.config.yaml.carry")
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("no carry record after migration: %v", err)
		}
		if err := legacy.Set("acct", KindOAuth, "v2"); err != nil {
			t.Fatal(err)
		}
		late := &Store{backend: backend, dir: s.dir, kr: s.kr, now: func() time.Time { return time.Now().Add(carryWindow + time.Hour) }}
		if err := late.scopeTo("config.yaml"); err != nil {
			t.Fatal(err)
		}
		if got, _ := late.Get("acct", KindOAuth); got != "v1" {
			t.Fatalf("carried %q after the window closed", got)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("carry record left after the window closed: %v", err)
		}
	})
}
