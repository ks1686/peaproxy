// Package usage records per-request stats for the Showcase and Health pages.
package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const capEvents = 200

// Event is one completed (or failed) proxy call. Secrets must never be stored.
type Event struct {
	Time             time.Time `json:"time"`
	AccountID        string    `json:"accountId"`
	Model            string    `json:"model"`
	Protocol         string    `json:"protocol"`
	Stream           bool      `json:"stream"`
	PromptTokens     int       `json:"promptTokens,omitempty"`
	CompletionTokens int       `json:"completionTokens,omitempty"`
	Status           int       `json:"status"`
	Error            string    `json:"error,omitempty"`
	Preview          string    `json:"preview,omitempty"`
}

// AccountRollup is a per-account summary.
type AccountRollup struct {
	AccountID string `json:"accountId"`
	Calls     int    `json:"calls"`
	Errors    int    `json:"errors"`
	Tokens    int    `json:"tokens"`
}

type diskFile struct {
	Events []Event `json:"events"`
}

// Store is a ring buffer persisted as JSON when Path is set.
type Store struct {
	mu         sync.Mutex
	events     []Event
	path       string
	requestLog string
}

// Open loads events from path if the file exists.
func Open(path string) *Store {
	s := &Store{path: path}
	if path == "" {
		return s
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var disk diskFile
	if json.Unmarshal(b, &disk) == nil {
		s.events = disk.Events
		if len(s.events) > capEvents {
			s.events = s.events[len(s.events)-capEvents:]
		}
	}
	return s
}

// SetRequestLog enables an append-only redacted JSONL log.
func (s *Store) SetRequestLog(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestLog = path
}

// Add appends an event, dropping the oldest when full, and persists.
func (s *Store) Add(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Preview = Redact(e.Preview)
	e.Error = Redact(e.Error)
	if len(e.Preview) > 160 {
		e.Preview = e.Preview[:160] + "…"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) >= capEvents {
		s.events = append(s.events[1:], e)
	} else {
		s.events = append(s.events, e)
	}
	s.flushLocked()
	s.appendLogLocked(e)
}

func (s *Store) flushLocked() {
	if s.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o700)
	b, err := json.MarshalIndent(diskFile{Events: s.events}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.path, b, 0o600)
}

func (s *Store) appendLogLocked(e Event) {
	if s.requestLog == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.requestLog), 0o700)
	f, err := os.OpenFile(s.requestLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
}

// Recent returns newest-first copies.
func (s *Store) Recent() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.events))
	for i := range s.events {
		out[len(s.events)-1-i] = s.events[i]
	}
	return out
}

// ByAccount rolls up counts.
func (s *Store) ByAccount() []AccountRollup {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := map[string]int{}
	var out []AccountRollup
	for _, e := range s.events {
		i, ok := idx[e.AccountID]
		if !ok {
			i = len(out)
			idx[e.AccountID] = i
			out = append(out, AccountRollup{AccountID: e.AccountID})
		}
		out[i].Calls++
		out[i].Tokens += e.PromptTokens + e.CompletionTokens
		if e.Status >= 400 || e.Error != "" {
			out[i].Errors++
		}
	}
	return out
}

// Path is the usage.json file, if any.
func (s *Store) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// Redact strips bearer tokens and x-api-key values.
func Redact(s string) string {
	if s == "" {
		return s
	}
	lower := strings.ToLower(s)
	for _, key := range []string{"bearer ", "sk-", "x-api-key", "api_key", "access_token", "refresh_token", "id_token"} {
		if strings.Contains(lower, key) {
			return "[redacted]"
		}
	}
	return s
}
