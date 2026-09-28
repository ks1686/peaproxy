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
