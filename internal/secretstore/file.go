package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/fslock"
	"github.com/ks1686/peaproxy/internal/sizehint"
)

type fileBlob struct {
	V     int               `json:"v"`
	Items map[string]string `json:"items"`
}

func (s *Store) fileSet(key, value string) error {
	blob, err := s.loadBlob(false)
	if err != nil {
		return err
	}
	blob.Items[key] = value
	return s.saveBlob(blob)
}

func (s *Store) fileGet(key string, shared bool) (string, error) {
	blob, err := s.loadBlob(shared)
	if err != nil {
		return "", err
	}
	v, ok := blob.Items[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (s *Store) fileDelete(key string) error {
	blob, err := s.loadBlob(false)
	if err != nil {
		return err
	}
	if _, ok := blob.Items[key]; !ok {
		return nil
	}
	delete(blob.Items, key)
	return s.saveBlob(blob)
}

func (s *Store) filePrune(keep map[string]struct{}) error {
	blob, err := s.loadBlob(false)
	if err != nil {
		return err
	}
	changed := false
	for k := range blob.Items {
		if _, ok := keep[accountOf(k)]; ok {
			continue
		}
		delete(blob.Items, k)
		changed = true
	}
	if !changed {
		return nil
	}
	return s.saveBlob(blob)
}

// loadBlob reads and decrypts the blob. shared asks for the retrying key read,
// which only the unlocked Get fallback may do; every other caller holds the
// lock (#64).
func (s *Store) loadBlob(shared bool) (fileBlob, error) {
	empty := fileBlob{V: 1, Items: map[string]string{}}
	raw, err := os.ReadFile(s.encPath())
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return fileBlob{}, err
	}
	key, err := s.keyFor(shared)
	if err != nil {
		return fileBlob{}, err
	}
	plain, err := decrypt(key, raw)
	if err != nil {
		return fileBlob{}, err
	}
	var blob fileBlob
	if err := json.Unmarshal(plain, &blob); err != nil {
		return fileBlob{}, err
	}
	if blob.Items == nil {
		blob.Items = map[string]string{}
	}
	if blob.V == 0 {
		blob.V = 1
	}
	return blob, nil
}

func (s *Store) saveBlob(blob fileBlob) error {
	if blob.Items == nil {
		blob.Items = map[string]string{}
	}
	blob.V = 1
	plain, err := json.Marshal(blob)
	if err != nil {
		return err
	}
	key, err := s.loadOrCreateKey()
	if err != nil {
		return err
	}
	enc, err := encrypt(key, plain)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, EncryptedFileName+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := f.Write(enc)
	if werr == nil {
		werr = f.Sync()
	}
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	if err := fslock.Rename(f.Name(), s.encPath()); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}

// keySettleTime is how long a wrong-sized key file is treated as "another
// process is still writing it". Past it, the file is simply broken. It only
// applies to loadOrCreateKeyShared, and only on the unlocked fallback path (#64).
const keySettleTime = time.Second

// loadOrCreateKey reads secret.key, creating it if absent. It assumes
// secrets.lock is held, so no other process is writing the file and a
// wrong-sized one is simply broken. It reports that immediately rather than
// retrying: the retry this replaces held two locks for half a second to reach
// the same error (#64).
func (s *Store) loadOrCreateKey() ([]byte, error) {
	b, err := os.ReadFile(s.keyPath())
	switch {
	case err == nil && len(b) == 32:
		return b, nil
	case err == nil:
		return nil, badKeyError(s.keyPath(), len(b))
	case !os.IsNotExist(err):
		return nil, err
	}
	key, err := s.createKey()
	if errors.Is(err, fs.ErrExist) {
		// Somebody created it without taking the lock. One re-read settles it.
		b, rerr := os.ReadFile(s.keyPath())
		if rerr != nil {
			return nil, rerr
		}
		if len(b) != 32 {
			return nil, badKeyError(s.keyPath(), len(b))
		}
		return b, nil
	}
	return key, err
}

// loadOrCreateKeyShared is loadOrCreateKey for the one path that cannot rely on
// the lock: Get's fallback to an unlocked read when secrets.lock will not open
// for writing (permission, read-only filesystem).
//
// That fallback exists because this process is not necessarily the only writer
// here, so a key file caught mid-creation is real and worth waiting for. It is
// the surviving reason for this retry -- not a pre-v2.0.10 binary, which no
// longer exists (#64).
func (s *Store) loadOrCreateKeyShared() ([]byte, error) {
	const attempts = 50
	for attempt := 1; ; attempt++ {
		b, err := os.ReadFile(s.keyPath())
		switch {
		case err == nil && len(b) == 32:
			return b, nil
		case err == nil && attempt < attempts && !settled(s.keyPath()):
			time.Sleep(10 * time.Millisecond)
			continue
		case err == nil:
			return nil, badKeyError(s.keyPath(), len(b))
		case !os.IsNotExist(err):
			return nil, err
		}
		key, err := s.createKey()
		if !errors.Is(err, fs.ErrExist) || attempt >= attempts {
			return key, err
		}
	}
}

func badKeyError(path string, n int) error {
	return fmt.Errorf("secretstore: %s is %d bytes, not 32; it is the key every stored secret is encrypted with, so delete it only if you are willing to re-enter them -- PeaProxy generates a new one on the next run", path, n)
}

// settled reports whether a wrong-sized key file has been sitting there long
// enough that it is not about to become correct. Only loadOrCreateKeyShared
// asks. An mtime we cannot read counts as settled: the fallback is an error
// with a clear message, which beats holding two locks to repeat it.
func settled(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return time.Since(info.ModTime()) > keySettleTime
}

func (s *Store) createKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.keyPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_, werr := f.Write(key)
	if werr == nil {
		werr = f.Sync()
	}
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = os.Remove(s.keyPath())
		return nil, err
	}
	return key, nil
}

func encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ad := []byte(Magic)
	out := make([]byte, 0, sizehint.Sum(len(Magic), len(nonce), len(plaintext), gcm.Overhead()))
	out = append(out, Magic...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, ad), nil
}

func decrypt(key, raw []byte) ([]byte, error) {
	if !strings.HasPrefix(string(raw), Magic) {
		return nil, errors.New("secretstore: unrecognized secrets.enc magic")
	}
	raw = raw[len(Magic):]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("secretstore: truncated secrets.enc")
	}
	nonce, ciphertext := raw[:ns], raw[ns:]
	return gcm.Open(nil, nonce, ciphertext, []byte(Magic))
}

// keyFor picks the key read that matches the caller's locking.
func (s *Store) keyFor(shared bool) ([]byte, error) {
	if shared {
		return s.loadOrCreateKeyShared()
	}
	return s.loadOrCreateKey()
}
