package eval

import (
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/usage"
)

func f64(v float64) *float64 { return &v }

// Each scenario is a promise a user would be entitled to rely on. They are
// written as claims rather than as code paths, because a gate phrased as "the
// router works" tells a future reader nothing when it fails.
func routingPromises() []Scenario {
	const plain = `{"model":"pea/economy","messages":[{"role":"user","content":"explain the build failure"}]}`
	const withTools = `{"model":"pea/economy","messages":[{"role":"user","content":"run the tests"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`

	return []Scenario{
		{
			Name:    "economy prefers the cheapest deployment",
			Why:     "The headline promise is spending less. If this regresses, automatic routing is decoration.",
			Model:   "pea/economy",
			Request: plain,
			Deployments: []Deployment{
				{ID: "cheap", Input: 1, Output: 2},
				{ID: "dear", Input: 30, Output: 60},
			},
			WantAccount: "cheap",
		},
		{
			Name:    "an explicitly selected model is never substituted",
			Why:     "Substituting a cheaper model for one a user named would be silent degradation dressed as a saving.",
			Model:   "m",
			Request: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			Deployments: []Deployment{
				{ID: "only", Input: 50, Output: 50},
			},
			WantAccount: "only",
		},
		{
			Name:    "free-only refuses a metered deployment",
			Why:     "A user who forbade spending must get a refusal, not a bill.",
			Model:   "pea/economy",
			Request: plain,
			Deployments: []Deployment{
				{ID: "paid", Input: 2, Output: 4},
			},
			Configure: func(c *config.Config) {
				yes := true
				c.Optimization.FreeOnly = &yes
			},
			WantFailure: true,
		},
		{
			Name:    "free-only still serves a free deployment",
			Why:     "When the budget is gone, a free request is the one thing the user can still have.",
			Model:   "pea/economy",
			Request: plain,
			Deployments: []Deployment{
				{ID: "freeacct", Input: 0, Output: 0, Free: true},
			},
			Configure: func(c *config.Config) {
				yes := true
				c.Optimization.FreeOnly = &yes
			},
			WantAccount: "freeacct",
		},
		{
			Name:    "an unknown price never wins over a known one",
			Why:     "Treating unknown as cheap is how a proxy spends money it did not know it was spending.",
			Model:   "pea/economy",
			Request: plain,
			Deployments: []Deployment{
				{ID: "known", Input: 5, Output: 5},
				{ID: "unpriced"},
			},
			Configure: func(c *config.Config) {
				// Remove the quote for one account, leaving it unpriced.
				delete(c.AutomaticRoutes.Prices, "unpriced/m")
			},
			WantAccount: "known",
		},
		{
			Name:    "a request needing tools is not served by a tool-less deployment",
			Why:     "Sending a tool-using request somewhere that cannot run tools loses the call. This regressed as D9.",
			Model:   "pea/economy",
			Request: withTools,
			Deployments: []Deployment{
				{ID: "withtools", Input: 1, Output: 1, Tools: true},
			},
			WantAccount: "withtools",
		},
		{
			Name:    "an unmeasured spend total does not satisfy a ceiling",
			Why:     "Failing open here spends money on exactly the accounts whose prices are least known.",
			Model:   "pea/economy",
			Request: plain,
			Deployments: []Deployment{
				{ID: "paid", Input: 1, Output: 1},
			},
			Configure: func(c *config.Config) {
				c.Optimization.SpendCeilingUSD = 100
			},
			SeedUsage: []usage.Event{
				{Time: time.Now(), AccountID: "paid", Model: "m", CostUSD: f64(1)},
				{Time: time.Now(), AccountID: "paid", Model: "m"},
			},
			WantFailure: true,
		},
		{
			Name:    "the caller's own tools survive routing",
			Why:     "PeaProxy adds capabilities to a request; it must never remove what the client asked for.",
			Model:   "pea/economy",
			Request: withTools,
			Deployments: []Deployment{
				{ID: "withtools", Input: 1, Output: 1, Tools: true},
			},
			WantAccount:  "withtools",
			WantContains: `"name":"lookup"`,
			WantAbsent:   "",
		},
	}
}

// Cost is asserted as well as behaviour, because a gate that only checks which
// account answered cannot see a regression where the right account is chosen
// for the wrong reason.
func TestRoutingPromises(t *testing.T) {
	scenarios := routingPromises()
	RunAll(t, scenarios)

	for _, s := range scenarios {
		if s.Why == "" {
			t.Errorf("scenario %q has no stated consequence; a gate nobody can explain is a gate nobody will fix", s.Name)
		}
	}
}

// The cheaper deployment must actually be cheaper in recorded cost, not merely
// preferred. This is the claim the whole product rests on, stated as
// arithmetic so it cannot quietly become true by definition.
func TestCheaperChoiceReallyIsCheaper(t *testing.T) {
	cheap := Run(t, routingPromises()[0])
	dear := costOf(routingPromises()[0].Deployments, "dear", 1000, 500)
	if cheap.Cost >= dear {
		t.Fatalf("the chosen deployment cost %.6f, not less than the alternative's %.6f", cheap.Cost, dear)
	}
	t.Logf("economy route saved $%.6f of $%.6f on a 1000/500-token request",
		dear-cheap.Cost, dear)
}
