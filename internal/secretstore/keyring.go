package secretstore

import (
	"encoding/json"
	"errors"
	"os"

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

func (s *Store) keyringSet(key, value string) error {
	if s.kr == nil {
		return errors.New("secretstore: keyring not configured")
	}
	if err := s.kr.Set(Service, key, value); err != nil {
		return err
	}
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	idx[key] = struct{}{}
	return s.saveIndex(idx)
}

func (s *Store) keyringGet(key string) (string, error) {
	if s.kr == nil {
		return "", ErrNotFound
	}
	v, err := s.kr.Get(Service, key)
	if err != nil {
		if isNotFound(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	return v, nil
}

func (s *Store) keyringDelete(key string) error {
	if s.kr == nil {
		return nil
	}
	err := s.kr.Delete(Service, key)
	if err != nil && !isNotFound(err) {
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
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	for k := range idx {
		if _, ok := keep[accountOf(k)]; ok {
			continue
		}
		if err := s.kr.Delete(Service, k); err != nil && !isNotFound(err) {
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
