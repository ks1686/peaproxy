package usage

import (
	"testing"
	"time"
)

// A spend ceiling is only a ceiling if it survives a restart. The event ring is
// deliberately bounded, so a ceiling computed from it would quietly reset on
// every restart -- exactly the situation a ceiling exists for.
//
// The window is therefore day-granular, computed from the persisted rollups.
// A true 24-hour rolling window would have to either count a whole boundary day
// it should not, or fall back to the ring and undercount when the ring does not
// reach back far enough. Undercounting a ceiling fails open, which is the
// dangerous direction.
func TestSpentInLastDaysComesFromDurableRollups(t *testing.T) {
	s := Open("")
	today := time.Now().UTC()
	for i := 0; i < 3; i++ {
		s.Add(Event{Time: today, CostUSD: f64(1.0)})
	}

	if w := s.SpentInLastDays(1); w.Priced != 3 || w.Total != 3 || w.USD != 3.0 {
		t.Fatalf("today = %v, %d priced of %d calls; want 3.0, 3 of 3", w.USD, w.Priced, w.Total)
	}
	if w := s.SpentInLastDays(3); w.Total != 3 {
		t.Fatalf("a three-day window lost calls: %d", w.Total)
	}
}

// Spend from before the window must not count, or the ceiling is wrong in the
// direction that spends the user's money.
func TestSpentInLastDaysIgnoresOlderDays(t *testing.T) {
	s := Open("")
	today := time.Now().UTC()
	s.Add(Event{Time: today, CostUSD: f64(2.0)})
	s.Add(Event{Time: today.AddDate(0, 0, -5), CostUSD: f64(100.0)})

	if w := s.SpentInLastDays(1); w.Priced != 1 || w.Total != 1 || w.USD != 2.0 {
		t.Fatalf("older spend leaked into the window: %v, %d priced of %d", w.USD, w.Priced, w.Total)
	}
	// A wider window legitimately sees it.
	if w := s.SpentInLastDays(7); w.USD != 102.0 {
		t.Fatalf("seven-day window = %v, want 102", w.USD)
	}
}

// Cost is only known when a provider published a price for the call. Unpriced
// usage must not be reported as zero spend, because a ceiling computed from it
// would look untouched while the bill grows. Counting such calls separately is
// what lets the caller decide whether the ceiling is trustworthy.
func TestSpentInLastDaysCountsOnlyPricedCalls(t *testing.T) {
	s := Open("")
	now := time.Now()
	s.Add(Event{Time: now, CostUSD: f64(3.0)})
	s.Add(Event{Time: now}) // no price published

	w := s.SpentInLastDays(1)
	if w.USD != 3.0 {
		t.Fatalf("known spend = %v, want 3", w.USD)
	}
	// The unpriced call is invisible in the total but visible in the count, and
	// that difference is what tells a caller the total is a floor.
	if w.Priced != 1 || w.Total != 2 {
		t.Fatalf("priced=%d total=%d, want 1 of 2", w.Priced, w.Total)
	}
}

// A call the provider refused cost nothing, and a failed call is not spend
// unless a cost was actually recorded.
func TestSpentInLastDaysIgnoresFailedUncostedCalls(t *testing.T) {
	s := Open("")
	s.Add(Event{Time: time.Now(), Status: 500, Error: "upstream refused"})

	if w := s.SpentInLastDays(1); w.USD != 0 || w.Priced != 0 {
		t.Fatalf("a failed uncosted call counted as spend: %v, %d priced", w.USD, w.Priced)
	}
}

func TestSpentInLastDaysEmptyIsZero(t *testing.T) {
	s := Open("")
	if w := s.SpentInLastDays(7); w.USD != 0 || w.Priced != 0 || w.Total != 0 {
		t.Fatalf("empty store reported %v, %d of %d", w.USD, w.Priced, w.Total)
	}
}

// A window of zero or fewer days has no meaning; it must report zero rather
// than summing everything.
func TestSpentInLastDaysRejectsNonPositiveWindows(t *testing.T) {
	s := Open("")
	s.Add(Event{Time: time.Now(), CostUSD: f64(5.0)})
	for _, n := range []int{0, -1} {
		if w := s.SpentInLastDays(n); w.USD != 0 || w.Priced != 0 || w.Total != 0 {
			t.Fatalf("window %d returned %v, %d of %d", n, w.USD, w.Priced, w.Total)
		}
	}
}

func f64(v float64) *float64 { return &v }

// A day with no priced calls still had calls. Dropping the whole day because
// its cost is unknown made `priced` equal `total` at zero, which the spend
// ceiling read as "measurably nothing spent" -- so a ceiling failed open
// precisely when nobody had priced anything.
func TestUnpricedDayStillCountsItsCalls(t *testing.T) {
	// Built from the current day rather than a literal: the window rolls, and a
	// fixed date would drop out of it tomorrow and fail for the wrong reason.
	today := time.Now().UTC().Format("2006-01-02")
	s := &Store{days: []DayRollup{
		{Day: today, Calls: 5, CostCalls: 0},
	}}

	w := s.SpentInLastDays(1)
	if w.Total != 5 {
		t.Errorf("total calls = %d, want 5; unpriced calls were dropped entirely", w.Total)
	}
	if w.Priced == w.Total {
		t.Errorf("priced (%d) == total (%d); an unmeasured day looks measurably free", w.Priced, w.Total)
	}
	if w.USD != 0 {
		t.Errorf("usd = %v, want 0 for an unpriced day", w.USD)
	}
}

// A day that is partly priced contributes both sides, so the caller can tell
// how much of the spend is actually known.
func TestPartlyPricedDayCountsEveryCall(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	cost := 1.5
	s := &Store{days: []DayRollup{
		{Day: today, Calls: 10, CostCalls: 4, CostUSD: &cost},
	}}

	w := s.SpentInLastDays(1)
	if w.USD != 1.5 {
		t.Errorf("usd = %v, want 1.5", w.USD)
	}
	if w.Priced != 4 || w.Total != 10 {
		t.Errorf("priced/total = %d/%d, want 4/10", w.Priced, w.Total)
	}
}
