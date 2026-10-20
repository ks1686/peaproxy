package secretstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ks1686/peaproxy/internal/fslock"
)

// carryWindow is how long after a migration secrets the legacy store gains are
// still carried into the scope.
//
// The window exists for an upgrade with the old server still running: any new
// command migrates, and the old server goes on refreshing tokens into the
// legacy store until it is restarted. Without carrying, the new server starts
// on refresh tokens the old one already spent.
const carryWindow = 7 * 24 * time.Hour

// carryRecord is what the scope and the legacy store agreed on at migration.
type carryRecord struct {
	Since time.Time `json:"since"`
	// Track maps a key to the SHA-256 of the value both stores last held. A
	// key leaves it once the scope's value differs: the scope was written or
	// pruned since, and from then on it wins.
	Track map[string]string `json:"track"`
	// Seen lists every legacy key the scope has already considered, so a
	// secret the scope pruned is not taken back from the never-pruned legacy
	// store.
	Seen []string `json:"seen"`
}

func (s *Store) carryPath() string {
	return filepath.Join(s.dir, scopedName("secrets.carry", s.scope))
}

func digest(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// startCarryLocked records the values just copied by a migration.
func (s *Store) startCarryLocked(legacy *Store) error {
	keys, err := legacy.keysLocked()
	if err != nil {
		return err
	}
	rec := carryRecord{Since: s.clock().UTC(), Track: map[string]string{}, Seen: keys}
	for _, k := range keys {
		if v, err := s.getLocked(k); err == nil {
			rec.Track[k] = digest(v)
		}
	}
	return s.saveCarry(rec)
}

// carryLocked brings legacy secrets that changed or appeared since the
// migration into the scope, under the rules on carryRecord. It is best effort:
// a legacy store it cannot read is retried on the next open rather than
// failing the config load.
func (s *Store) carryLocked(legacy *Store) error {
	b, err := os.ReadFile(s.carryPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var rec carryRecord
	if json.Unmarshal(b, &rec) != nil || s.clock().Sub(rec.Since) > carryWindow {
		return os.Remove(s.carryPath())
	}
	legacyKeys, err := legacy.keysLocked()
	if err != nil {
		return nil
	}
	if rec.Track == nil {
		rec.Track = map[string]string{}
	}
	changed := false
	for k, h := range rec.Track {
		if sv, err := s.getLocked(k); err != nil || digest(sv) != h {
			delete(rec.Track, k)
			changed = true
			continue
		}
		lv, err := legacy.getLocked(k)
		if err != nil {
			continue
		}
		if lh := digest(lv); lh != h {
			if err := s.setLocked(k, lv); err != nil {
				return err
			}
			rec.Track[k] = lh
			changed = true
		}
	}
	seen := make(map[string]bool, len(rec.Seen))
	for _, k := range rec.Seen {
		seen[k] = true
	}
	for _, k := range legacyKeys {
		if seen[k] {
			continue
		}
		rec.Seen = append(rec.Seen, k)
		changed = true
		if _, err := s.getLocked(k); !errors.Is(err, ErrNotFound) {
			continue
		}
		lv, err := legacy.getLocked(k)
		if err != nil {
			continue
		}
		if err := s.setLocked(k, lv); err != nil {
			return err
		}
		rec.Track[k] = digest(lv)
	}
	if len(rec.Track) == 0 {
		return os.Remove(s.carryPath())
	}
	if !changed {
		return nil
	}
	return s.saveCarry(rec)
}

func (s *Store) saveCarry(rec carryRecord) error {
	sort.Strings(rec.Seen)
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return fslock.WriteAtomic(s.carryPath(), b, 0o600)
}

func (s *Store) getLocked(k string) (string, error) {
	if s.backend == BackendKeyring {
		return s.keyringGet(k)
	}
	return s.fileGet(k, false)
}

func (s *Store) setLocked(k, v string) error {
	if s.backend == BackendKeyring {
		return s.keyringSet(k, v)
	}
	return s.fileSet(k, v)
}

// keysLocked lists the item keys this store holds.
func (s *Store) keysLocked() ([]string, error) {
	var keys []string
	if s.backend == BackendKeyring {
		idx, err := s.loadIndex()
		if err != nil {
			return nil, err
		}
		for k := range idx.keys {
			keys = append(keys, k)
		}
	} else {
		if !fileExists(s.encPath()) {
			return nil, nil
		}
		blob, err := s.loadBlob(false)
		if err != nil {
			return nil, err
		}
		for k := range blob.Items {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}
