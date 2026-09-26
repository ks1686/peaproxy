// Package usage records per-request stats for the Showcase and Health pages.
package usage

import (
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

// Store is an in-memory ring buffer. CPA removed built-in usage; PeaProxy keeps it.
type Store struct {
	mu     sync.Mutex
	events []Event
}

// Add appends an event, dropping the oldest when full.
func (s *Store) Add(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if len(e.Preview) > 160 {
		e.Preview = e.Preview[:160] + "…"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) >= capEvents {
		s.events = append(s.events[1:], e)
		return
	}
	s.events = append(s.events, e)
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
