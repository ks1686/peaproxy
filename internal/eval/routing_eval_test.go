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
			Name:    "economy weighs output, not only input",
			Why:     "The rates are crossed on purpose. An input-only comparison picks the wrong deployment here, and a user with a long answer would pay more than the promise says.",
			Model:   "pea/economy",
			Request: plain,
			Deployments: []Deployment{
				// 1000 input + 500 output: A costs 0.031, B costs 0.021.
				{ID: "chatty", Input: 1, Output: 60},
				{ID: "concise", Input: 20, Output: 2},
			},
			WantAccount: "concise",
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
				// The tool-less deployment is cheaper, so if eligibility ever
				// stops being applied this scenario picks it and fails. With a
				// single capable deployment it could not fail at all.
				{ID: "ignorant", Input: 1, Output: 1, Tools: Boolp(false)},
				{ID: "withtools", Input: 2, Output: 2, Tools: Boolp(true)},
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
			Name:    "free-only refuses a named model too",
			Why:     "Naming a model is not a way around the one switch a user set to protect their account. This path never consulted freeOnly.",
			Model:   "m",
			Request: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
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
			Name:    "the spend ceiling reaches a named model too",
			Why:     "The ceiling was checked only on the automatic path, so a client naming a model bypassed the budget entirely.",
			Model:   "m",
			Request: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			Deployments: []Deployment{
				{ID: "paid", Input: 2, Output: 4},
			},
			Configure: func(c *config.Config) {
				c.Optimization.SpendCeilingUSD = 1
			},
			SeedUsage: []usage.Event{
				{Time: time.Now(), AccountID: "paid", Model: "m", CostUSD: f64(5)},
			},
			WantFailure: true,
		},
		{
			Name:    "spend measured from tokens alone does not satisfy a ceiling",
			Why:     "Only half a call's usage is knowable before the answer arrives. A ceiling that trusted a one-sided measurement would be trusting a total that understates the bill.",
			Model:   "pea/economy",
			Request: plain,
			Deployments: []Deployment{
				{ID: "paid", Input: 1, Output: 1},
			},
			Configure: func(c *config.Config) {
				c.Optimization.SpendCeilingUSD = 100
			},
			SeedUsage: []usage.Event{
				{Time: time.Now(), AccountID: "paid", Model: "m",
					TokensKnown: true, PromptTokens: 1000, CompletionTokens: 500},
			},
			WantFailure: true,
		},
		{
			Name:    "warmth never beats a cheaper deployment",
			Why:     "Warmth is a tie-breaker between deployments price leaves equal. Judged on the input rate alone it picks the deployment that is cheap to prompt with and expensive to read from, which is the expensive one for any answer worth having.",
			Model:   "pea/economy",
			Request: `{"model":"pea/economy","messages":[{"role":"user","content":"hi"}]}`,
			Deployments: []Deployment{
				// The cheaper deployment is declared first, because warmth is only
				// ever consulted for the candidates *after* the one price picked.
				// Declared the other way round this scenario would pass whatever
				// the warmth code did, because warmth would never be asked.
				{ID: "cold", Input: 20, Output: 2},
				// Warm but dear on output. The rates cross, so only a comparison
				// that weighs both of them keeps the cold one.
				{ID: "warm", Input: 1, Output: 60},
			},
			SeedUsage: []usage.Event{
				{Time: time.Now(), AccountID: "warm", Model: "m", CacheRead: 900},
			},
			WantAccount: "cold",
		},
		{
			Name:    "warmth decides when price cannot",
			Why:     "Two deployments priced identically have nothing left to separate them, and a warm prefix is a real prior about the next turn -- no provider reports it, but the recent cache read is evidence PeaProxy does have.",
			Model:   "pea/economy",
			Request: `{"model":"pea/economy","messages":[{"role":"user","content":"hi"}]}`,
			Deployments: []Deployment{
				// Equal prices leave nothing to separate them, so the first
				// declaration would win outright if warmth did nothing at all.
				{ID: "cold", Input: 1, Output: 2},
				{ID: "warm", Input: 1, Output: 2},
			},
			SeedUsage: []usage.Event{
				{Time: time.Now(), AccountID: "warm", Model: "m", CacheRead: 900},
			},
			WantAccount: "warm",
		},
		{
			Name:    "a route refuses a provider with no account attached",
			Why:     "A keyless provider is somebody else's machine. The setting that says whether a prompt may go there was read by nothing, so every automatic route published to it.",
			Model:   "pea/auto",
			Request: plain,
			Deployments: []Deployment{
				{ID: "stranger", Input: 1, Output: 1},
			},
			Configure: func(c *config.Config) {
				for i := range c.Providers {
					c.Providers[i].APIKey = ""
					c.Providers[i].APIKeyEnv = ""
				}
			},
			WantFailure: true,
		},
		{
			Name:    "the caller's own tools survive routing",
			Why:     "PeaProxy adds capabilities to a request; it must never remove what the client asked for.",
			Model:   "pea/economy",
			Request: withTools,
			Deployments: []Deployment{
				{ID: "withtools", Input: 1, Output: 1, Tools: Boolp(true)},
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
