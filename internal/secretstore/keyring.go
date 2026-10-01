package secretstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/zalando/go-keyring"
)

const probePrefix = "_peaproxy_probe."

// probeUser is unique per call, so concurrent Opens in any number of
// processes never read or delete each other's probe item.
func probeUser() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return probePrefix + strconv.Itoa(os.Getpid()) + "." + hex.EncodeToString(b[:]), nil
}

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
	user, err := probeUser()
	if err != nil {
		return err
	}
	if err := kr.Set(Service, user, "ok"); err != nil {
		return err
	}
	defer func() { _ = kr.Delete(Service, user) }()
	v, err := kr.Get(Service, user)
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

func (s *Store) writeChunks(key string, ref chunkRef, parts []string) error {
	for i, part := range parts {
		if err := s.kr.Set(Service, ref.key(key, i), part); err != nil {
			return err
		}
	}
	return nil
}

// keyringSet records both generations as pending (and the key as indexed)
// before touching the keyring, so a crash at any later point leaves an entry
// a future sweep can use. On a handled failure the new generation is dropped
// and the old one stays live; on success the old one is dropped.
func (s *Store) keyringSet(key, value string) error {
	if s.kr == nil {
		return errors.New("secretstore: keyring not configured")
	}
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	s.sweep(idx)
	old, err := s.replaceableChunks(key)
	if err != nil {
		return err
	}
	var fresh chunkRef
	var parts []string
	if needsChunks(value) {
		if fresh.gen, err = newGen(old.gen); err != nil {
			return err
		}
		parts = splitChunks(value)
		fresh.n = len(parts)
	}
	mine := s.intend(idx, key, old, fresh)
	idx.keys[key] = struct{}{}
	if err := s.saveIndex(idx); err != nil {
		return err
	}
	stored := value
	if fresh.n > 0 {
		if err := s.writeChunks(key, fresh, parts); err != nil {
			_ = s.settle(idx, mine, old)
			return err
		}
		stored = fresh.header()
	}
	if err := s.kr.Set(Service, key, stored); err != nil {
		_ = s.settle(idx, mine, old)
		return err
	}
	return s.settle(idx, mine, fresh)
}

// keyringGet reads a secret, re-reading the header if the generation it is
// following is replaced underneath it.
//
// This retry is not legacy cruft. secrets.lock lives in the config directory
// but the keychain is per-user, so two PeaProxy instances with different configs
// share every item under Service and share no lock at all. One of them can
// publish a new generation and delete the old chunks while this read is mid-way
// through them. (#64 assumed only a pre-v2.0.10 binary could do this; any two
// current instances can.)
func (s *Store) keyringGet(key string) (string, error) {
	if s.kr == nil {
		return "", ErrNotFound
	}
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
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	s.sweep(idx)
	if err := s.deleteEntry(key); err != nil {
		return err
	}
	delete(idx.keys, key)
	return s.saveIndex(idx)
}

func (s *Store) keyringPrune(keep map[string]struct{}) error {
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	s.sweep(idx)
	for k := range idx.keys {
		if _, ok := keep[accountOf(k)]; ok {
			continue
		}
		if err := s.deleteEntry(k); err != nil {
			return err
		}
		delete(idx.keys, k)
	}
	return s.saveIndex(idx)
}
