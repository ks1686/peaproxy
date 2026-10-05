// Package contextstore holds request artifacts that PeaProxy needs to carry
// context between turns: retrieved passages, tool output it chose to keep, and
// intermediate results of its own small local computations.
//
// What it is not is a portable cache. A provider's KV cache, hidden reasoning,
// signed blocks and response ids cannot be moved between providers, so nothing
// here is treated as reproducing one. These artifacts are PeaProxy's own notes
// and are re-derived, never replayed.
//
// Storage is in memory and bounded. Persistence is opt-in and off by default.
package contextstore

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// Default bounds. They are in the plan and are the reason this package is not
// allowed to grow a "temporary" unbounded slice anywhere.
const (
	DefaultSessionBytes = 64 << 20
	DefaultProcessBytes = 256 << 20
	DefaultIdleTTL      = time.Hour
)

// ErrTooLarge is returned when a single artifact exceeds the session budget.
var ErrTooLarge = errors.New("artifact exceeds the session budget")

// ErrStoreFull is returned when storing would push the process over its cap and
// nothing could be evicted to make room.
var ErrStoreFull = errors.New("context store is full")

// Options are the bounds. Zero values take the defaults.
type Options struct {
	SessionBytes int
	ProcessBytes int
	IdleTTL      time.Duration
	// now is the clock, overridable in tests.
	now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.SessionBytes <= 0 {
		o.SessionBytes = DefaultSessionBytes
	}
	if o.ProcessBytes <= 0 {
		o.ProcessBytes = DefaultProcessBytes
	}
	if o.IdleTTL <= 0 {
		o.IdleTTL = DefaultIdleTTL
	}
	if o.now == nil {
		o.now = time.Now
	}
	return o
}

// Artifact is one stored item. Kind and Source are recorded so a later consumer
// can tell what it is holding and where it came from, rather than guessing.
type Artifact struct {
	Key  string
	Kind string
	// Source names what produced this. It is metadata for humans; nothing in
	// this package trusts it.
	Source string
	Body   []byte
	// Portable records whether this artifact means anything outside the
	// provider that produced it. Cached provider state is not portable, and a
	// consumer must not carry one to a different provider.
	Portable bool

	storedAt time.Time
	touched  time.Time
}

type sessionState struct {
	items map[string]Artifact
	bytes int
}

type Store struct {
	mu       sync.Mutex
	opts     Options
	sessions map[string]*sessionState
	bytes    int
}

// New builds an in-memory store with the given bounds.
func New(opts Options) *Store {
	return &Store{opts: opts.withDefaults(), sessions: map[string]*sessionState{}}
}

// Put stores an artifact, replacing any existing one with the same key.
//
// Replacing frees the old bytes first, so rewriting the same artifact each turn
// -- the ordinary case for a growing conversation -- does not ratchet a session
// toward its cap.
func (s *Store) Put(session string, a Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	a.Body = append([]byte(nil), a.Body...)
	now := s.opts.now()
	a.storedAt = now
	a.touched = now
	size := len(a.Body)

	sess := s.sessions[session]
	if sess == nil {
		sess = &sessionState{items: map[string]Artifact{}}
		s.sessions[session] = sess
	}
	if prev, ok := sess.items[a.Key]; ok {
		sess.bytes -= len(prev.Body)
		s.bytes -= len(prev.Body)
		delete(sess.items, a.Key)
	}

	if size > s.opts.SessionBytes {
		if len(sess.items) == 0 {
			delete(s.sessions, session)
		}
		return ErrTooLarge
	}

	s.evictLocked(session, sess, size)
	if size > s.opts.SessionBytes-sess.bytes || size > s.opts.ProcessBytes-s.bytes {
		return ErrStoreFull
	}

	sess.items[a.Key] = a
	sess.bytes += size
	s.bytes += size
	return nil
}

// Get returns a copy of an artifact. Expired artifacts are removed and reported
// as absent rather than returned.
func (s *Store) Get(sessID, key string) (Artifact, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess := s.sessions[sessID]
	if sess == nil {
		return Artifact{}, false
	}
	a, ok := sess.items[key]
	if !ok {
		return Artifact{}, false
	}
	if s.expired(a) {
		s.dropLocked(sessID, sess, key)
		return Artifact{}, false
	}
	// Two copies, deliberately. The caller gets one it may mutate freely; the
	// store keeps the original and only has its idle timer refreshed. Handing
	// back the same slice that is stored would let a caller rewrite the store.
	caller := a
	caller.Body = append([]byte(nil), a.Body...)
	stored := sess.items[key]
	stored.touched = s.opts.now()
	sess.items[key] = stored
	return caller, true
}

// evictLocked makes room for size bytes in this session, dropping the least
// recently used artifacts. An artifact currently being replaced is already gone.
func (s *Store) evictLocked(sessID string, sess *sessionState, size int) {
	// Expire first: idle artifacts are the cheapest thing to reclaim.
	now := s.opts.now()
	for key, a := range sess.items {
		if now.Sub(a.touched) > s.opts.IdleTTL {
			s.dropLocked(sessID, sess, key)
		}
	}
	if size <= s.opts.SessionBytes-sess.bytes && size <= s.opts.ProcessBytes-s.bytes {
		return
	}

	keys := make([]string, 0, len(sess.items))
	for k := range sess.items {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return sess.items[keys[i]].touched.Before(sess.items[keys[j]].touched)
	})
	for _, k := range keys {
		if size <= s.opts.SessionBytes-sess.bytes && size <= s.opts.ProcessBytes-s.bytes {
			return
		}
		s.dropLocked(sessID, sess, k)
	}
}

func (s *Store) dropLocked(sessID string, sess *sessionState, key string) {
	a, ok := sess.items[key]
	if !ok {
		return
	}
	sess.bytes -= len(a.Body)
	s.bytes -= len(a.Body)
	delete(sess.items, key)
	if sess.bytes <= 0 && len(sess.items) == 0 {
		delete(s.sessions, sessID)
	}
}

func (s *Store) expired(a Artifact) bool {
	return s.opts.now().Sub(a.touched) > s.opts.IdleTTL
}

// SessionBytes reports what one session currently holds.
func (s *Store) SessionBytes(session string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess := s.sessions[session]; sess != nil {
		return sess.bytes
	}
	return 0
}

// ProcessBytes reports what every session currently holds together.
func (s *Store) ProcessBytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes
}

// DropSession forgets everything for one session.
func (s *Store) DropSession(sessID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[sessID]
	if sess == nil {
		return
	}
	for _, a := range sess.items {
		s.bytes -= len(a.Body)
	}
	delete(s.sessions, sessID)
}

// List returns a session's artifacts, newest touch first then by key, so
// iteration order is stable. Callers get copies.
func (s *Store) List(session string) []Artifact {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[session]
	if sess == nil {
		return nil
	}
	out := make([]Artifact, 0, len(sess.items))
	for _, a := range sess.items {
		a.Body = append([]byte(nil), a.Body...)
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].touched.Equal(out[j].touched) {
			return out[i].touched.After(out[j].touched)
		}
		return out[i].Key < out[j].Key
	})
	return out
}
