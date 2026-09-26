package quota

import (
	"net/http"
	"sync"
)

// Store keeps the latest remaining snapshot per account. Observe is a short
// mutex write so adapters can call it from the HTTP transport without extra I/O.
type Store struct {
	mu     sync.Mutex
	byAcct map[string]Snapshot
}

// NewStore returns an empty in-memory store.
func NewStore() *Store {
	return &Store{byAcct: map[string]Snapshot{}}
}

// Observe records remaining headers for an account. Empty/unknown headers are
// ignored so a later blank response cannot wipe a real snapshot or invent 0.
func (s *Store) Observe(accountID, adapter string, h http.Header) {
	if s == nil || accountID == "" {
		return
	}
	parsed := ParseHeaders(h)
	if !parsed.Reported() {
		return
	}
	parsed.AccountID = accountID
	parsed.Adapter = adapter
	parsed.Note = "from upstream response headers"
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byAcct == nil {
		s.byAcct = map[string]Snapshot{}
	}
	prev := s.byAcct[accountID]
	s.byAcct[accountID] = mergeHeader(prev, parsed)
}

// ApplyProbe merges a documented usage/quota probe onto the account snapshot.
func (s *Store) ApplyProbe(snap Snapshot) {
	if s == nil || snap.AccountID == "" || !snap.Reported() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byAcct == nil {
		s.byAcct = map[string]Snapshot{}
	}
	prev := s.byAcct[snap.AccountID]
	s.byAcct[snap.AccountID] = mergeProbe(prev, snap)
}

// Get returns the stored snapshot, if any.
func (s *Store) Get(accountID string) (Snapshot, bool) {
	if s == nil {
		return Snapshot{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.byAcct[accountID]
	return snap, ok
}

// Account is one configured provider used to build the Health/quota list.
type Account struct {
	ID      string
	Adapter string
}

// Views returns one row per account. Unknown remaining stays omitted.
func (s *Store) Views(accounts []Account) []Snapshot {
	out := make([]Snapshot, 0, len(accounts))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range accounts {
		fam := Lookup(a.Adapter)
		row := Snapshot{
			AccountID: a.ID,
			Adapter:   a.Adapter,
			Source:    SourceNone,
			Note:      fam.Note,
		}
		if stored, ok := s.byAcct[a.ID]; ok && stored.Reported() {
			row = stored
			if row.Adapter == "" {
				row.Adapter = a.Adapter
			}
			if row.AccountID == "" {
				row.AccountID = a.ID
			}
		}
		out = append(out, row)
	}
	if out == nil {
		out = []Snapshot{}
	}
	return out
}

func mergeHeader(prev, next Snapshot) Snapshot {
	out := prev
	out.AccountID = next.AccountID
	out.Adapter = next.Adapter
	out.Source = SourceHeaders
	out.Note = next.Note
	out.CapturedAt = next.CapturedAt
	if next.Model != "" {
		out.Model = next.Model
	}
	copyInt := func(dst **int64, src *int64) {
		if src != nil {
			*dst = src
		}
	}
	copyInt(&out.RemainingRequests, next.RemainingRequests)
	copyInt(&out.LimitRequests, next.LimitRequests)
	copyInt(&out.RemainingTokens, next.RemainingTokens)
	copyInt(&out.LimitTokens, next.LimitTokens)
	copyInt(&out.RemainingProjectTokens, next.RemainingProjectTokens)
	copyInt(&out.LimitProjectTokens, next.LimitProjectTokens)
	copyInt(&out.RemainingRequestsDay, next.RemainingRequestsDay)
	copyInt(&out.LimitRequestsDay, next.LimitRequestsDay)
	copyInt(&out.RemainingRequestsMinute, next.RemainingRequestsMinute)
	copyInt(&out.LimitRequestsMinute, next.LimitRequestsMinute)
	copyInt(&out.RemainingTokensMinute, next.RemainingTokensMinute)
	copyInt(&out.LimitTokensMinute, next.LimitTokensMinute)
	copyInt(&out.RemainingInputTokens, next.RemainingInputTokens)
	copyInt(&out.LimitInputTokens, next.LimitInputTokens)
	copyInt(&out.RemainingOutputTokens, next.RemainingOutputTokens)
	copyInt(&out.LimitOutputTokens, next.LimitOutputTokens)
	copyInt(&out.RemainingTokensPrompt, next.RemainingTokensPrompt)
	copyInt(&out.RemainingTokensGenerated, next.RemainingTokensGenerated)
	if next.ResetRequests != "" {
		out.ResetRequests = next.ResetRequests
	}
	if next.ResetTokens != "" {
		out.ResetTokens = next.ResetTokens
	}
	if next.ResetProjectTokens != "" {
		out.ResetProjectTokens = next.ResetProjectTokens
	}
	if next.ResetRequestsDay != "" {
		out.ResetRequestsDay = next.ResetRequestsDay
	}
	if next.ResetRequestsMinute != "" {
		out.ResetRequestsMinute = next.ResetRequestsMinute
	}
	if next.ResetTokensMinute != "" {
		out.ResetTokensMinute = next.ResetTokensMinute
	}
	return out
}

func mergeProbe(prev, next Snapshot) Snapshot {
	out := prev
	out.AccountID = next.AccountID
	if next.Adapter != "" {
		out.Adapter = next.Adapter
	}
	out.Source = SourceProbe
	out.Note = next.Note
	out.CapturedAt = next.CapturedAt
	if out.CapturedAt == nil {
		out.CapturedAt = nowUTC()
	}
	out.RemainingCredits = next.RemainingCredits
	out.LimitCredits = next.LimitCredits
	out.UsageCredits = next.UsageCredits
	out.CreditsUnlimited = next.CreditsUnlimited
	return out
}
