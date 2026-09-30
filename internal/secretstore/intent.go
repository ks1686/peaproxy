package secretstore

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/fslock"
)

// The index doubles as an intent log. Before a write creates or replaces a
// chunk generation it records "pending:<unix>:<key>#<gen>:<n>" for each
// generation involved, and forgets the entry once the losing generation is
// deleted. A process killed in between leaves the entry behind, so a later
// write can still find and delete the orphaned chunks (go-keyring cannot
// enumerate items). Entries share the JSON string array with base keys, so
// index files written before this change load unchanged.
const pendingPrefix = "pending:"

// pendingGrace keeps a sweep from deleting a generation that a pre-v2.0.10
// binary, which does not take secrets.lock, may still be writing.
const pendingGrace = 30 * time.Minute

type pending struct {
	key string
	ref chunkRef
	at  int64 // unix seconds when recorded
}

func (p pending) String() string {
	return pendingPrefix + strconv.FormatInt(p.at, 10) + ":" + p.key + "#" + p.ref.gen + ":" + strconv.Itoa(p.ref.n)
}

func parsePending(s string) (pending, bool) {
	rest, ok := strings.CutPrefix(s, pendingPrefix)
	if !ok {
		return pending{}, false
	}
	at, rest, ok := strings.Cut(rest, ":")
	i := strings.LastIndex(rest, "#")
	if !ok || i < 1 {
		return pending{}, false
	}
	gen, count, ok := strings.Cut(rest[i+1:], ":")
	ts, terr := strconv.ParseInt(at, 10, 64)
	n, nerr := strconv.Atoi(count)
	if !ok || terr != nil || nerr != nil || n < 1 {
		return pending{}, false
	}
	return pending{key: rest[:i], ref: chunkRef{gen: gen, n: n}, at: ts}, true
}

type index struct {
	keys    map[string]struct{}
	pending map[pending]struct{}
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// intend records every non-empty generation in gens as pending for key.
func (s *Store) intend(idx index, key string, gens ...chunkRef) []pending {
	at := s.clock().Unix()
	var mine []pending
	for _, ref := range gens {
		if ref.n > 0 {
			p := pending{key: key, ref: ref, at: at}
			idx.pending[p] = struct{}{}
			mine = append(mine, p)
		}
	}
	return mine
}

// settle deletes every generation in mine except live and forgets it. An
// entry whose chunks cannot be deleted stays in the index for a later sweep.
func (s *Store) settle(idx index, mine []pending, live chunkRef) error {
	if len(mine) == 0 {
		return nil
	}
	var errs []error
	for _, p := range mine {
		if live.n == 0 || p.ref.gen != live.gen {
			if err := s.dropChunks(p.key, p.ref); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		delete(idx.pending, p)
	}
	return errors.Join(append(errs, s.saveIndex(idx))...)
}

// sweep deletes pending generations that no header points to. Entries younger
// than pendingGrace, or whose header or chunks cannot be read or deleted right
// now, are kept for a later sweep.
func (s *Store) sweep(idx index) {
	cutoff := s.clock().Add(-pendingGrace).Unix()
	for p := range idx.pending {
		if p.at > cutoff {
			continue
		}
		live, err := s.replaceableChunks(p.key)
		if err != nil {
			continue
		}
		if live.n > 0 && live.gen == p.ref.gen {
			delete(idx.pending, p)
			continue
		}
		if s.dropChunks(p.key, p.ref) == nil {
			delete(idx.pending, p)
		}
	}
}

func (s *Store) loadIndex() (index, error) {
	idx := index{keys: map[string]struct{}{}, pending: map[pending]struct{}{}}
	b, err := os.ReadFile(s.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return idx, nil
		}
		return index{}, err
	}
	var entries []string
	if err := json.Unmarshal(b, &entries); err != nil {
		return index{}, err
	}
	for _, e := range entries {
		if p, ok := parsePending(e); ok {
			idx.pending[p] = struct{}{}
		} else if e != "" && !strings.HasPrefix(e, pendingPrefix) {
			idx.keys[e] = struct{}{}
		}
	}
	return idx, nil
}

// saveIndex replaces the file atomically: a torn index would fail every
// later load, and with it every keyring write.
func (s *Store) saveIndex(idx index) error {
	entries := make([]string, 0, len(idx.keys)+len(idx.pending))
	for k := range idx.keys {
		entries = append(entries, k)
	}
	for p := range idx.pending {
		entries = append(entries, p.String())
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, IndexFileName+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	if err := fslock.Rename(f.Name(), s.indexPath()); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}
