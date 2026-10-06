package catalog

import "testing"

func TestFreeExcludesUnknownPrice(t *testing.T) {
	zero := 0.0
	if (Price{Input: &zero, Output: &zero, Verified: false}).Free() {
		t.Fatal("unverified zero was treated as free")
	}
	if (Price{}).Free() {
		t.Fatal("missing price was treated as free")
	}
	if !(Price{Input: &zero, Output: &zero, Verified: true}).Free() {
		t.Fatal("verified zero was not free")
	}
}

// A deployment that publishes a paid cache rate is not free, even when its
// input and output rates are zero. Cached turns are billed separately on every
// provider that documents them, so a zero input rate says nothing about them,
// and free-only routing promises the user's money is safe.
func TestFreeExcludesAPaidCacheRate(t *testing.T) {
	zero, paid := 0.0, 1.25
	for name, p := range map[string]Price{
		"cache read":  {Input: &zero, Output: &zero, CacheRead: &paid, Verified: true},
		"cache write": {Input: &zero, Output: &zero, CacheWrite: &paid, Verified: true},
	} {
		if p.Free() {
			t.Errorf("%s: a paid cache rate was read as free", name)
		}
	}
}

// An unpublished cache rate is not evidence of a charge, and refusing it would
// disable free-only routing for every provider that does not document cache
// pricing at all.
func TestFreeIgnoresUnpublishedCacheRates(t *testing.T) {
	zero := 0.0
	if !(Price{Input: &zero, Output: &zero, Verified: true}).Free() {
		t.Fatal("a deployment whose cache rates are unpublished stopped being free")
	}
}

func TestUnknownPriceDoesNotWinEconomy(t *testing.T) {
	one := 1.0
	known := Price{Input: &one, Output: &one, Verified: true}
	if Cheaper(Price{}, known) {
		t.Fatal("unknown price beat a known price")
	}
	if !Cheaper(known, Price{}) {
		t.Fatal("known price did not beat unknown")
	}
}

// Comparing input rates alone picks the wrong deployment whenever the rates
// cross, and the mistake is invisible until a user is billed for a long answer.
func TestCheaperWeighsOutputWhenRatesCross(t *testing.T) {
	promptCheap, answerCostly := 1.0, 60.0
	promptCostly, answerCheap := 20.0, 2.0

	// Lower input rate, far higher output rate: the trap.
	a := Price{Input: &promptCheap, Output: &answerCostly, Verified: true}
	b := Price{Input: &promptCostly, Output: &answerCheap, Verified: true}
	if Cheaper(a, b) {
		t.Error("the cheaper-to-prompt deployment won despite costing four times as much to read from")
	}
	if !Cheaper(b, a) {
		t.Error("the deployment that is cheaper on both rates did not win")
	}
}

// A deployment that is not worse on either rate wins without any weighting.
func TestCheaperPrefersPlainlyCheaper(t *testing.T) {
	cheapIn, cheapOut, dearIn, dearOut := 1.0, 2.0, 30.0, 60.0
	cheap := Price{Input: &cheapIn, Output: &cheapOut, Verified: true}
	dear := Price{Input: &dearIn, Output: &dearOut, Verified: true}
	if !Cheaper(cheap, dear) || Cheaper(dear, cheap) {
		t.Fatal("the plainly cheaper deployment did not win on both rates")
	}
}

// Equal on input and dearer on output: output decides.
func TestCheaperBreaksAnInputTieOnOutput(t *testing.T) {
	in, dearOut, cheapOut := 5.0, 30.0, 3.0
	cheapOutPrice := Price{Input: &in, Output: &cheapOut, Verified: true}
	dearOutPrice := Price{Input: &in, Output: &dearOut, Verified: true}
	if !Cheaper(cheapOutPrice, dearOutPrice) || Cheaper(dearOutPrice, cheapOutPrice) {
		t.Fatal("an input tie was not broken by the output rate")
	}
}

// The same deployment on both sides is not cheaper than itself; otherwise
// ranking can reorder candidates without a reason.
func TestCheaperIsNotCheaperThanItself(t *testing.T) {
	in, out := 5.0, 5.0
	p := Price{Input: &in, Output: &out, Verified: true}
	if Cheaper(p, p) {
		t.Fatal("a deployment beat itself")
	}
}

// NotDearer is the comparison warmth makes. It must answer the same question
// economy ranking answers, or warmth quietly reinstates the input-only trap.
//
// NotDearer is dominance, not a blend: a candidate that is worse on any
// published rate is not "not dearer". Crossed rates therefore answer false in
// both directions, and warmth declines to decide -- which is the safe answer,
// because there is no cheapness to preserve by switching.
func TestNotDearerComparesBothRates(t *testing.T) {
	cheapIn, dearOut := 1.0, 60.0
	dearIn, cheapOut := 20.0, 2.0
	crossed := Price{Input: &cheapIn, Output: &dearOut, Verified: true}
	other := Price{Input: &dearIn, Output: &cheapOut, Verified: true}

	if NotDearer(crossed, other) {
		t.Error("a deployment dearer on output was called not dearer because its input rate is lower")
	}
	if NotDearer(other, crossed) {
		t.Error("a deployment dearer on input was called not dearer because its output rate is lower")
	}

	// Genuinely not worse on either rate is the case warmth acts on.
	same, dearer := 2.0, 9.0
	better := Price{Input: &same, Output: &same, Verified: true}
	worse := Price{Input: &dearer, Output: &dearer, Verified: true}
	if !NotDearer(better, worse) || NotDearer(worse, better) {
		t.Error("a plainly cheaper deployment was not reported as not dearer")
	}

	// An unknown price decides nothing in either direction.
	if NotDearer(Price{}, worse) || NotDearer(worse, Price{}) {
		t.Error("an unpriced deployment settled a comparison")
	}
}
