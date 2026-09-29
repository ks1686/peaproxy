package secretstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

const probeUser = "_peaproxy_probe"

type liveKeyring struct{}

func (liveKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (liveKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}

func (liveKeyring) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

func isNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, keyring.ErrNotFound)
}

func probeKeyring(kr keyringAPI) error {
	if err := kr.Set(Service, probeUser, "ok"); err != nil {
		return err
	}
	defer func() { _ = kr.Delete(Service, probeUser) }()
	v, err := kr.Get(Service, probeUser)
	if err != nil {
		return err
	}
	if v != "ok" {
		return errors.New("secretstore: keyring probe mismatch")
	}
	return nil
}

// currentChunks reads the base item's header. Only not-found means "no
// chunks"; any other read error propagates so callers never orphan chunks.
func (s *Store) currentChunks(key string) (chunkRef, error) {
	v, err := s.kr.Get(Service, key)
	if err != nil {
		if isNotFound(err) {
			return chunkRef{}, nil
		}
		return chunkRef{}, err
	}
	ref, _, err := parseHeader(key, v)
	return ref, err
}

// replaceableChunks is currentChunks for writers: a corrupt header cannot name
// its chunks, so it is overwritten or deleted instead of blocking re-login.
func (s *Store) replaceableChunks(key string) (chunkRef, error) {
	ref, err := s.currentChunks(key)
	if errors.Is(err, ErrUnreadable) {
		return chunkRef{}, nil
	}
	return ref, err
}

func (s *Store) dropChunks(key string, ref chunkRef) error {
	for i := 0; i < ref.n; i++ {
		if err := s.kr.Delete(Service, ref.key(key, i)); err != nil && !isNotFound(err) {
			return err
		}
	}
	return nil
}

// writeChunks stores value as a fresh generation. On failure it removes the
// chunks it already wrote (best effort) so nothing unindexed is left behind.
func (s *Store) writeChunks(key, oldGen, value string) (chunkRef, error) {
	gen, err := newGen(oldGen)
	if err != nil {
		return chunkRef{}, err
	}
	parts := splitChunks(value)
	for i, part := range parts {
		if err := s.kr.Set(Service, chunkRef{gen: gen}.key(key, i), part); err != nil {
			_ = s.dropChunks(key, chunkRef{gen: gen, n: i + 1})
			return chunkRef{}, err
		}
	}
	return chunkRef{gen: gen, n: len(parts)}, nil
}

func (s *Store) keyringSet(key, value string) error {
	if s.kr == nil {
		return errors.New("secretstore: keyring not configured")
	}
	defer lockDir(s.dir)()
	old, err := s.replaceableChunks(key)
	if err != nil {
		return err
	}
	stored := value
	var fresh chunkRef
	if needsChunks(value) {
		if fresh, err = s.writeChunks(key, old.gen, value); err != nil {
			return err
		}
		stored = fresh.header()
	}
	if err := s.kr.Set(Service, key, stored); err != nil {
		_ = s.dropChunks(key, fresh)
		return err
	}
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	idx[key] = struct{}{}
	if err := s.saveIndex(idx); err != nil {
		return err
	}
	return s.dropChunks(key, old)
}

func (s *Store) keyringGet(key string) (string, error) {
	if s.kr == nil {
		return "", ErrNotFound
	}
	defer lockDir(s.dir)()
	// Another process can publish a new generation and delete the one this
	// read is following; a vanished chunk under a changed header is retried.
	var last string
	for attempt := 0; attempt < 3; attempt++ {
		v, err := s.kr.Get(Service, key)
		if err != nil {
			if isNotFound(err) {
				return "", ErrNotFound
			}
			return "", err
		}
		if attempt > 0 && v == last {
			break
		}
		last = v
		ref, ok, err := parseHeader(key, v)
		if !ok || err != nil {
			return v, err
		}
		value, missing, err := s.readChunks(key, ref)
		if err != nil || missing < 0 {
			return value, err
		}
	}
	return "", fmt.Errorf("%w: missing chunk of %s", ErrUnreadable, key)
}

// readChunks joins ref's chunks. missing is the index of the first absent
// chunk, or -1 when every chunk was read.
func (s *Store) readChunks(key string, ref chunkRef) (value string, missing int, err error) {
	var b strings.Builder
	for i := 0; i < ref.n; i++ {
		part, err := s.kr.Get(Service, ref.key(key, i))
		if isNotFound(err) {
			return "", i, nil
		}
		if err != nil {
			return "", i, err
		}
		b.WriteString(part)
	}
	return b.String(), -1, nil
}

// deleteEntry drops chunks before the header so a failed delete can be retried.
func (s *Store) deleteEntry(key string) error {
	ref, err := s.replaceableChunks(key)
	if err != nil {
		return err
	}
	if err := s.dropChunks(key, ref); err != nil {
		return err
	}
	if err := s.kr.Delete(Service, key); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

func (s *Store) keyringDelete(key string) error {
	if s.kr == nil {
		return nil
	}
	defer lockDir(s.dir)()
	if err := s.deleteEntry(key); err != nil {
		return err
	}
	idx, ierr := s.loadIndex()
	if ierr != nil {
		return ierr
	}
	delete(idx, key)
	return s.saveIndex(idx)
}

func (s *Store) keyringPrune(keep map[string]struct{}) error {
	defer lockDir(s.dir)()
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	for k := range idx {
		if _, ok := keep[accountOf(k)]; ok {
			continue
		}
		if err := s.deleteEntry(k); err != nil {
			return err
		}
		delete(idx, k)
	}
	return s.saveIndex(idx)
}

func (s *Store) loadIndex() (map[string]struct{}, error) {
	out := map[string]struct{}{}
	b, err := os.ReadFile(s.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	var keys []string
	if err := json.Unmarshal(b, &keys); err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k != "" {
			out[k] = struct{}{}
		}
	}
	return out, nil
}

func (s *Store) saveIndex(idx map[string]struct{}) error {
	keys := make([]string, 0, len(idx))
	for k := range idx {
		keys = append(keys, k)
	}
	b, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	return os.WriteFile(s.indexPath(), b, 0o600)
}
