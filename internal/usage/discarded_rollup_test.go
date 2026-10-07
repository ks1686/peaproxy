package usage

import (
	"testing"
)

func pricedDiscard(acct string, usd float64) Event {
	e := Event{AccountID: acct, Model: "m", Status: 200, PromptTokens: 10, Discarded: true}
	c := usd
	e.CostUSD = &c
	return e
}

func estimatedDiscard(acct string, usd float64) Event {
	e := Event{AccountID: acct, Model: "m", Status: 200, PromptTokens: 10, Discarded: true}
	v := usd
	e.EstimatedUSD = &v
	return e
}

func unpricedDiscard(acct string) Event {
	return Event{AccountID: acct, Model: "m", Status: 200, PromptTokens: 10, Discarded: true}
}

// An account can hold rounds PeaProxy priced, rounds a provider priced, and a
// round neither could price. The unpriced one makes the money unknown -- but
// "unknown" has to be the answer whatever order the events arrived in.
//
// Clearing the totals when the unpriced round is seen made the answer depend on
// sequence: a priced round arriving afterwards re-initialised the pointers and
// produced a confident $0.0045 for an account whose total was never knowable.
func TestAnUnpricedDiscardIsUnknownWhicheverOrderTheRoundsArriveIn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func() []Event
	}{
		{"unpriced then priced", func() []Event {
			return []Event{unpricedDiscard("acct"), estimatedDiscard("acct", 0.0045)}
		}},
		{"priced then unpriced", func() []Event {
			return []Event{estimatedDiscard("acct", 0.0045), unpricedDiscard("acct")}
		}},
		{"priced, unpriced, priced again", func() []Event {
			return []Event{pricedDiscard("acct", 0.001), unpricedDiscard("acct"), estimatedDiscard("acct", 0.0045)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Store{}
			for _, e := range tc.build() {
				s.Add(e)
			}
			rows := s.ByAccount()
			if len(rows) != 1 {
				t.Fatalf("got %d accounts, want 1", len(rows))
			}
			r := rows[0]
			// The counts stay true regardless: three rounds happened and
			// their tokens were spent, whatever they cost.
			if r.Discarded != len(tc.build()) {
				t.Fatalf("counted %d discarded rounds, want %d", r.Discarded, len(tc.build()))
			}
			if r.DiscardedTokens != 10*len(tc.build()) {
				t.Fatalf("attributed %d tokens, want %d", r.DiscardedTokens, 10*len(tc.build()))
			}
			if r.DiscardedUSD != nil {
				t.Fatalf("states %v for an account holding a round nobody could price", *r.DiscardedUSD)
			}
			if r.DiscardedEstimatedUSD != nil {
				t.Fatalf("states %v for an account holding a round nobody could price; a partial "+
					"sum reads as a smaller total, which is a claim this rollup must not make",
					*r.DiscardedEstimatedUSD)
			}
		})
	}
}

// An account whose discarded rounds are all priced states both figures, kept
// apart: a provider's number is the bill and an estimate is a reading of it, so
// summing them would count the same tokens twice.
func TestPricedDiscardRoundsReportTheBillAndTheEstimateSeparately(t *testing.T) {
	s := &Store{}
	s.Add(pricedDiscard("acct", 0.002))
	s.Add(estimatedDiscard("acct", 0.0045))

	rows := s.ByAccount()
	r := rows[0]
	if r.DiscardedUSD == nil || *r.DiscardedUSD != 0.002 {
		t.Fatalf("provider figure = %v, want 0.002", r.DiscardedUSD)
	}
	if r.DiscardedEstimatedUSD == nil || *r.DiscardedEstimatedUSD != 0.0045 {
		t.Fatalf("estimate = %v, want 0.0045", r.DiscardedEstimatedUSD)
	}
	if r.DiscardedEstimatedCalls != 1 {
		t.Fatalf("counted %d estimated rounds, want 1", r.DiscardedEstimatedCalls)
	}
}

// One account's unpriced round must not blank another account's figure: the
// unknown belongs to the account that has it.
func TestAnUnpricedRoundDoesNotBlankAnotherAccountsTotal(t *testing.T) {
	s := &Store{}
	s.Add(pricedDiscard("healthy", 0.003))
	s.Add(unpricedDiscard("broken"))

	for _, r := range s.ByAccount() {
		switch r.AccountID {
		case "healthy":
			if r.DiscardedUSD == nil || *r.DiscardedUSD != 0.003 {
				t.Fatalf("the priced account reports %v, want 0.003: an unknown on another "+
					"account is not this account's", r.DiscardedUSD)
			}
		case "broken":
			if r.DiscardedUSD != nil {
				t.Fatalf("the unpriced account states %v, want unknown", *r.DiscardedUSD)
			}
			if r.Discarded != 1 {
				t.Fatalf("the unpriced account counted %d rounds, want 1: an unpriced round is still a round", r.Discarded)
			}
		}
	}
}
