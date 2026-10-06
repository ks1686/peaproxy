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

	if got, priced, total := s.SpentInLastDays(1); priced != 3 || total != 3 || got != 3.0 {
		t.Fatalf("today = %v, %d priced of %d calls; want 3.0, 3 of 3", got, priced, total)
	}
	if _, _, total := s.SpentInLastDays(3); total != 3 {
		t.Fatalf("a three-day window lost calls: %d", total)
	}
}

// Spend from before the window must not count, or the ceiling is wrong in the
// direction that spends the user's money.
func TestSpentInLastDaysIgnoresOlderDays(t *testing.T) {
	s := Open("")
	today := time.Now().UTC()
	s.Add(Event{Time: today, CostUSD: f64(2.0)})
	s.Add(Event{Time: today.AddDate(0, 0, -5), CostUSD: f64(100.0)})

	if got, priced, total := s.SpentInLastDays(1); priced != 1 || total != 1 || got != 2.0 {
		t.Fatalf("older spend leaked into the window: %v, %d priced of %d", got, priced, total)
	}
	// A wider window legitimately sees it.
	if got, _, _ := s.SpentInLastDays(7); got != 102.0 {
		t.Fatalf("seven-day window = %v, want 102", got)
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

	got, priced, total := s.SpentInLastDays(1)
	if got != 3.0 {
		t.Fatalf("known spend = %v, want 3", got)
	}
	// The unpriced call is invisible in the total but visible in the count, and
	// that difference is what tells a caller the total is a floor.
	if priced != 1 || total != 2 {
		t.Fatalf("priced=%d total=%d, want 1 of 2", priced, total)
	}
}

// A call the provider refused cost nothing, and a failed call is not spend
// unless a cost was actually recorded.
func TestSpentInLastDaysIgnoresFailedUncostedCalls(t *testing.T) {
	s := Open("")
	s.Add(Event{Time: time.Now(), Status: 500, Error: "upstream refused"})

	if got, priced, _ := s.SpentInLastDays(1); got != 0 || priced != 0 {
		t.Fatalf("a failed uncosted call counted as spend: %v, %d priced", got, priced)
	}
}

func TestSpentInLastDaysEmptyIsZero(t *testing.T) {
	s := Open("")
	if got, priced, total := s.SpentInLastDays(7); got != 0 || priced != 0 || total != 0 {
		t.Fatalf("empty store reported %v, %d of %d", got, priced, total)
	}
}

// A window of zero or fewer days has no meaning; it must report zero rather
// than summing everything.
func TestSpentInLastDaysRejectsNonPositiveWindows(t *testing.T) {
	s := Open("")
	s.Add(Event{Time: time.Now(), CostUSD: f64(5.0)})
	for _, n := range []int{0, -1} {
		if got, priced, total := s.SpentInLastDays(n); got != 0 || priced != 0 || total != 0 {
			t.Fatalf("window %d returned %v, %d of %d", n, got, priced, total)
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

	usd, priced, total := s.SpentInLastDays(1)
	if total != 5 {
		t.Errorf("total calls = %d, want 5; unpriced calls were dropped entirely", total)
	}
	if priced == total {
		t.Errorf("priced (%d) == total (%d); an unmeasured day looks measurably free", priced, total)
	}
	if usd != 0 {
		t.Errorf("usd = %v, want 0 for an unpriced day", usd)
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

	usd, priced, total := s.SpentInLastDays(1)
	if usd != 1.5 {
		t.Errorf("usd = %v, want 1.5", usd)
	}
	if priced != 4 || total != 10 {
		t.Errorf("priced/total = %d/%d, want 4/10", priced, total)
	}
}
