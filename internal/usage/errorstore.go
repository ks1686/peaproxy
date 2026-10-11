package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The error store is a second, longer-lived view of the same Event records.
// It exists because usage.json keeps 200 and requests.log keeps the last 80
// after rotation, so a failure is evicted by traffic rather than by age, and an
// upstream 401 in a busy afternoon is gone by lunchtime.
//
// What it writes is deliberately narrower than Event: no prompt preview. This
// file is meant to leave the machine, and a failed request's prompt is normally
// something the user already knows they sent. Copying it in buys no diagnosis
// and costs the privacy that makes the file safe to hand over.

// SetErrorLog points the store at an error file. An empty path disables it,
// which is what a config without request logging gets.
func (s *Store) SetErrorLog(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errorLog = path
}

// ErrorLogPath is the error store this Store writes to, if enabled.
func (s *Store) ErrorLogPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errorLog
}

// AddError appends one record. A clean success is dropped rather than stored:
// the file is for things that were not fine, and a 200 line in it is noise that
// pushes a real failure out of the window.
//
// Every write redacts. That is not defensive styling: an upstream 401 quotes
// the offending credential in prose, and this file exists to be uploaded.
func (s *Store) AddError(e Event) {
	errorMu.Lock()
	defer errorMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.errorLog == "" || e.Class == FailureNone {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e = redactErrorEvent(e)
	_ = os.MkdirAll(filepath.Dir(s.errorLog), 0o700)
	if err := s.appendErrorLocked(e); err != nil {
		return
	}
	_ = s.pruneErrorLocked(time.Now())
}

func (s *Store) appendErrorLocked(e Event) error {
	f, err := os.OpenFile(s.errorLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	// Set on create; an existing file keeps whatever mode it had, and this one
	// holds provider error text that is still worth keeping private.
	_ = os.Chmod(s.errorLog, 0o600)
	return nil
}

// pruneErrorLocked drops records past the retention window. It runs after every
// append, so a quiet week still empties the file rather than leaving it stale
// forever. Callers hold errorMu, so it uses the unlocked variant.
func (s *Store) pruneErrorLocked(now time.Time) error {
	_, err := pruneErrorLogLocked(s.errorLog, ErrorCutoff(now, ErrorRetention()))
	return err
}

// ReadErrors returns the records newer than since, and repairs the file on disk
// so a stale entry does not survive being merely filtered out of one read.
func (s *Store) ReadErrors(since time.Time) ([]Event, error) {
	errorMu.Lock()
	defer errorMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.errorLog == "" {
		return nil, nil
	}
	if err := s.pruneErrorLocked(time.Now()); err != nil {
		return nil, err
	}
	all, err := readErrorLog(s.errorLog)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range all {
		if !e.Time.Before(since) {
			out = append(out, e)
		}
	}
	return out, nil
}

// ReadErrorRecords reads an error store without a Store. The export command
// runs in a separate process from the daemon, so it opens the file the daemon
// writes rather than sharing its handle.
func ReadErrorRecords(path string) ([]Event, error) {
	return readErrorLog(path)
}

func readErrorLog(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Event
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// pruneErrorLog rewrites path with only the records inside the window. Records
// are appended in time order, so this is a prefix scan in the common case and a
// full rewrite only when something actually aged out.
func pruneErrorLog(path string, cutoff time.Time) ([]Event, error) {
	errorMu.Lock()
	defer errorMu.Unlock()
	return pruneErrorLogLocked(path, cutoff)
}

func pruneErrorLogLocked(path string, cutoff time.Time) ([]Event, error) {
	all, err := readErrorLog(path)
	if err != nil {
		return nil, err
	}
	kept := all[:0]
	for _, e := range all {
		// Inclusive: a record exactly on the cutoff is still inside the window,
		// so two runs a second apart cannot disagree about it.
		if !e.Time.Before(cutoff) {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(all) {
		return kept, nil
	}
	if len(kept) == 0 {
		// Leave no file rather than an empty one: "no failures recorded" is
		// better answered by absence than by a zero-length file.
		_ = os.Remove(path)
		return nil, nil
	}
	var buf bytes.Buffer
	for _, e := range kept {
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return nil, err
	}
	return kept, nil
}

// redactErrorEvent strips what must not leave the machine and drops the prompt.
// The preview is removed rather than redacted because there is no diagnostic
// left in it that the rest of the record lacks.
func redactErrorEvent(e Event) Event {
	e.Error = clip(Redact(e.Error), maxPreview)
	e.Preview = ""
	return e
}

// ErrorSummary is the count block the export leads with, so a bundle can be read
// without scrolling every event.
type ErrorSummary struct {
	Failures int            `json:"failures"`
	Cancels  int            `json:"cancels"`
	ByClass  map[string]int `json:"byClass"`
	ByAcct   map[string]int `json:"byAccount"`
}

// SummarizeErrors counts records for the export header. Cancellations are
// counted apart from failures on purpose: 25 of 26 errors on a busy machine are
// clients disconnecting, and a count that cannot tell them apart is a count
// nobody reads.
func SummarizeErrors(recs []Event) ErrorSummary {
	s := ErrorSummary{ByClass: map[string]int{}, ByAcct: map[string]int{}}
	for _, e := range recs {
		s.ByClass[string(e.Class)]++
		if e.Class == ClassClientCancel {
			s.Cancels++
		} else {
			s.Failures++
		}
		if e.AccountID != "" {
			s.ByAcct[e.AccountID]++
		}
	}
	return s
}

// errorMu guards the error file across Store instances in one process. Appends
// and prunes are separate syscalls, so two stores would otherwise interleave
// them and each would prune the other's records.
var errorMu sync.Mutex
