package contextstore

import (
	"strings"
	"testing"
	"time"
)

// The store exists to carry context between turns, so it has to hold things.
// It is in a user's machine holding their prompts, so it has to hold them
// within limits a runaway cannot escape. These are the limits from the plan:
// 64 MiB per session, 256 MiB per process.
func TestSessionCapIsEnforced(t *testing.T) {
	s := New(Options{SessionBytes: 64 << 20, ProcessBytes: 256 << 20})
	// One artifact that is comfortably over the per-session cap.
	big := strings.Repeat("a", 70<<20)
	if err := s.Put("s1", Artifact{Key: "k", Body: []byte(big)}); err == nil {
		t.Fatal("an artifact larger than the session cap was stored")
	}
	if got := s.SessionBytes("s1"); got != 0 {
		t.Fatalf("session holds %d bytes after a refused store", got)
	}
}

// A session must not be able to consume the whole process budget either, even
// across many artifacts that each fit. Eviction rather than refusal is the
// right response here -- there is always something old to drop -- so the test
// pins the invariant that matters: the cap is never exceeded, however much the
// caller offers.
func TestProcessCapIsNeverExceeded(t *testing.T) {
	s := New(Options{SessionBytes: 64 << 20, ProcessBytes: 8 << 20})
	chunk := strings.Repeat("b", 1<<20)
	for i := 0; i < 64; i++ {
		key := string(rune('a'+i%26)) + string(rune('a'+i/26))
		if err := s.Put("s1", Artifact{Key: key, Body: []byte(chunk)}); err != nil {
			t.Fatalf("iteration %d refused a 1 MiB artifact under an 8 MiB cap: %v", i, err)
		}
		if got := s.ProcessBytes(); got > 8<<20 {
			t.Fatalf("after %d artifacts the process holds %d bytes, over the 8 MiB cap", i+1, got)
		}
		if got := s.SessionBytes("s1"); got > 64<<20 {
			t.Fatalf("session holds %d bytes, over its cap", got)
		}
	}
}

// Idle artifacts expire. A context that outlives the conversation it belongs to
// is a liability, not a help.
func TestIdleArtifactsExpire(t *testing.T) {
	now := time.Now()
	s := New(Options{SessionBytes: 64 << 20, ProcessBytes: 256 << 20, IdleTTL: time.Hour})
	if err := s.Put("s1", Artifact{Key: "k", Body: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	s.opts.now = func() time.Time { return now }

	if _, ok := s.Get("s1", "k"); !ok {
		t.Fatal("a fresh artifact was missing")
	}
	// Inside the window.
	s.opts.now = func() time.Time { return now.Add(59 * time.Minute) }
	if _, ok := s.Get("s1", "k"); !ok {
		t.Fatal("an artifact expired before its idle window elapsed")
	}
	// Reading refreshes the idle timer, so the window restarts from the last
	// access rather than from the write.
	s.opts.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, ok := s.Get("s1", "k"); ok {
		t.Fatal("an artifact outlived its idle window")
	}
	if s.SessionBytes("s1") != 0 {
		t.Fatal("an expired artifact still counted against the session budget")
	}
}

// Sessions are separate. One conversation's context must never be visible to
// another, or a shared gateway leaks prompts between users.
func TestSessionsAreIsolated(t *testing.T) {
	s := New(Options{SessionBytes: 64 << 20, ProcessBytes: 256 << 20})
	if err := s.Put("alice", Artifact{Key: "k", Body: []byte("alice's secret plan")}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("bob", "k"); ok {
		t.Fatal("one session read another's artifact")
	}
}

// Reading must hand back a copy. A caller mutating the returned slice must not
// corrupt what is stored, and must not be able to reach the store's buffer.
func TestGetReturnsACopy(t *testing.T) {
	s := New(Options{SessionBytes: 64 << 20, ProcessBytes: 256 << 20})
	if err := s.Put("s1", Artifact{Key: "k", Body: []byte("original")}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get("s1", "k")
	if !ok {
		t.Fatal("artifact missing")
	}
	mutated := got.Body
	mutated[0] = 'X'

	again, _ := s.Get("s1", "k")
	if string(again.Body) != "original" {
		t.Fatalf("mutating a returned artifact changed the store: %q", again.Body)
	}
}

// Replacing a key does not double-count, or a session slowly inflates as the
// same logical artifact is rewritten each turn.
func TestReplacingAnArtifactDoesNotDoubleCount(t *testing.T) {
	s := New(Options{SessionBytes: 64 << 20, ProcessBytes: 256 << 20})
	body := []byte(strings.Repeat("z", 1024))
	for i := 0; i < 10; i++ {
		if err := s.Put("s1", Artifact{Key: "k", Body: body}); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
	if got := s.SessionBytes("s1"); got != 1024 {
		t.Fatalf("session holds %d bytes for one 1 KiB artifact", got)
	}
}

// Eviction under pressure removes the least recently used artifact, and never
// an empty session or a non-existent one.
func TestEvictionDropsTheOldestArtifact(t *testing.T) {
	now := time.Now()
	s := New(Options{SessionBytes: 4 << 20, ProcessBytes: 256 << 20, IdleTTL: time.Hour})
	s.opts.now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		key := string(rune('a' + i))
		if err := s.Put("s1", Artifact{Key: key, Body: []byte(strings.Repeat("q", 1<<20))}); err != nil {
			t.Fatal(err)
		}
		s.opts.now = func() time.Time { return now.Add(time.Duration(i+1) * time.Second) }
	}
	// A fifth artifact must evict "a", the least recently used.
	if err := s.Put("s1", Artifact{Key: "e", Body: []byte(strings.Repeat("q", 1<<20))}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("s1", "a"); ok {
		t.Fatal("the oldest artifact survived eviction")
	}
	if _, ok := s.Get("s1", "b"); !ok {
		t.Fatal("eviction removed an artifact that was not the oldest")
	}
}
