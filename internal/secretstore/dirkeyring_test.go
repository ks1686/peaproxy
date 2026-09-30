package secretstore

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/fslock"
)

// dirKeyring is a keychain shared between processes: one file per item under
// root, replaced atomically. With pauseDir set, the first chunk Set creates
// pauseDir/paused and blocks until pauseDir/go exists, freezing the writer in
// the middle of keyringSet.
type dirKeyring struct {
	root     string
	pauseDir string
	paused   bool
}

func (k *dirKeyring) path(service, user string) string {
	return filepath.Join(k.root, hex.EncodeToString([]byte(service+"\x00"+user)))
}

func (k *dirKeyring) Set(service, user, password string) error {
	if k.pauseDir != "" && !k.paused && strings.Contains(user, "#") {
		k.paused = true
		if err := os.WriteFile(filepath.Join(k.pauseDir, "paused"), nil, 0o600); err != nil {
			return err
		}
		if err := waitForFile(filepath.Join(k.pauseDir, "go"), 30*time.Second); err != nil {
			return err
		}
	}
	f, err := os.CreateTemp(k.root, ".item.*.tmp")
	if err != nil {
		return err
	}
	_, werr := f.WriteString(password)
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return fslock.Rename(f.Name(), k.path(service, user))
}

func (k *dirKeyring) Get(service, user string) (string, error) {
	b, err := os.ReadFile(k.path(service, user))
	if os.IsNotExist(err) {
		return "", ErrNotFound
	}
	return string(b), err
}

func (k *dirKeyring) Delete(service, user string) error {
	err := os.Remove(k.path(service, user))
	if os.IsNotExist(err) {
		return ErrNotFound
	}
	return err
}

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for " + path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
