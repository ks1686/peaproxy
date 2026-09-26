package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type fileBlob struct {
	V     int               `json:"v"`
	Items map[string]string `json:"items"`
}

func (s *Store) fileSet(key, value string) error {
	blob, err := s.loadBlob()
	if err != nil {
		return err
	}
	blob.Items[key] = value
	return s.saveBlob(blob)
}

func (s *Store) fileGet(key string) (string, error) {
	blob, err := s.loadBlob()
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
	blob, err := s.loadBlob()
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
	blob, err := s.loadBlob()
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

func (s *Store) loadBlob() (fileBlob, error) {
	empty := fileBlob{V: 1, Items: map[string]string{}}
	raw, err := os.ReadFile(s.encPath())
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return fileBlob{}, err
	}
	key, err := s.loadOrCreateKey()
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
	tmp := s.encPath() + ".tmp"
	if err := os.WriteFile(tmp, enc, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.encPath())
}

func (s *Store) loadOrCreateKey() ([]byte, error) {
	b, err := os.ReadFile(s.keyPath())
	if err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("secretstore: %s must be 32 bytes", KeyFileName)
		}
		return b, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(s.keyPath(), key, 0o600); err != nil {
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
	out := make([]byte, 0, len(Magic)+len(nonce)+len(plaintext)+gcm.Overhead())
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
