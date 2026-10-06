package gateway

import (
	"testing"

	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/economics"
)

// A ceiling set to zero means no ceiling. It must never be read as "you have
// spent zero, so the budget is gone" -- that would break every user who set
// nothing, which is all of them by default.
func TestNoCeilingMeansNoCeiling(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	if gw.cfg.SpendCeiling() != 0 {
		t.Fatalf("default ceiling = %v, want 0", gw.cfg.SpendCeiling())
	}
	blocked, _ := gw.ceilingBlocks(paidUsage())
	if blocked {
		t.Fatal("a default configuration blocked a paid route")
	}
}

// Under the ceiling, paid routes still work. A ceiling that blocks ordinary
// spending below its limit is not a ceiling.
func TestCeilingAllowsSpendBelowTheLimit(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.Optimization.SpendCeilingUSD = 10

	blocked, reason := gw.ceilingBlocks(paidUsage())
	if blocked {
		t.Fatalf("blocked below the ceiling: %s", reason)
	}
}

// At the ceiling, the paid route is refused and the refusal explains itself.
func TestCeilingRefusesPaidRouteAtTheLimit(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.Optimization.SpendCeilingUSD = 5
	gw.spendWindow = spendWindow(func(int) (float64, int, int) { return 5.0, 10, 10 })

	blocked, reason := gw.ceilingBlocks(paidUsage())
	if !blocked {
		t.Fatal("spend at the ceiling was allowed")
	}
	if reason == "" {
		t.Fatal("a refusal must say why")
	}
}

// A free deployment must survive the ceiling. Refusing it would break the one
// thing the user can still have when the budget is gone.
func TestCeilingNeverRefusesAFreeDeployment(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.Optimization.SpendCeilingUSD = 1
	gw.spendWindow = spendWindow(func(int) (float64, int, int) { return 500.0, 500, 500 })

	zero := 0.0
	free := catalog.Model{ID: "free", AccountID: "acct-a", Tier: catalog.TierFree,
		Price: catalog.Price{Input: &zero, Output: &zero, Currency: "USD", Source: "openrouter", Verified: true}}
	gw.models = []catalog.Model{free}

	blocked, _ := gw.ceilingBlocks(freeUsage())
	if blocked {
		t.Fatal("the spend ceiling refused a free deployment")
	}
}

// This is the case that decides whether a ceiling means anything. When calls in
// the window carried no published price, recorded spend understates the bill.
// Letting the request through would be failing open on exactly the users most
// at risk, so the ceiling refuses and says it cannot vouch for the total.
func TestCeilingRefusesWhenSpendCannotBeMeasured(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.Optimization.SpendCeilingUSD = 100
	// Recorded spend is far below the ceiling, but half the calls were unpriced.
	gw.spendWindow = spendWindow(func(int) (float64, int, int) { return 1.0, 5, 8 })

	blocked, reason := gw.ceilingBlocks(paidUsage())
	if !blocked {
		t.Fatal("a ceiling trusted a total it could not measure")
	}
	if reason == "" {
		t.Fatal("an unmeasurable total must explain itself")
	}
}

// Once spend can be measured again, the ceiling stops blocking. A guard that
// latches shut is not a ceiling, it is an outage.
func TestCeilingRecoversWhenSpendIsMeasurable(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.Optimization.SpendCeilingUSD = 100
	gw.spendWindow = spendWindow(func(int) (float64, int, int) { return 1.0, 5, 8 })

	blocked, _ := gw.ceilingBlocks(paidUsage())
	if !blocked {
		t.Fatal("expected the unmeasurable case to block")
	}
	gw.spendWindow = spendWindow(func(int) (float64, int, int) { return 1.0, 8, 8 })

	blocked, _ = gw.ceilingBlocks(paidUsage())
	if blocked {
		t.Fatal("ceiling stayed shut after spend became measurable")
	}
}

func paidUsage() economics.Usage { return economics.Usage{Input: 1000, Output: 500} }
func freeUsage() economics.Usage { return economics.Usage{Input: 0, Output: 0} }
