// Package eval holds the gates a change has to pass before it ships.
//
// The promise this project makes is narrow and falsifiable: fulfil the same
// request for less. That is two claims, not one, and the second is the one
// that gets skipped. A cheaper answer is only a saving if it is the same
// answer -- so every scenario here checks cost *and* equivalence, and a
// scenario that gets cheaper by doing less work fails.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/contextstore"
	"github.com/ks1686/peaproxy/internal/gateway"
	"github.com/ks1686/peaproxy/internal/usage"
)

// Deployment is one account in a scenario, with what it costs and what it can do.
type Deployment struct {
	ID     string
	Input  float64
	Output float64
	Free   bool
	// Tools is a tri-state: nil keeps the adapter's declaration, and an explicit
	// false marks a deployment that accepts a tools array and ignores it.
	//
	// It is nullable for the same reason the config's capability overrides are.
	// A plain bool cannot distinguish "this scenario does not care" from "this
	// endpoint cannot run tools", and a scenario that cannot express the case it
	// claims to cover is a promise nobody can break -- which is worse than no
	// promise, because the gate reports it as covered.
	Tools *bool
	// Models are the model ids this deployment serves, advertised by its
	// /v1/models. An empty list means the single id "m".
	//
	// It exists so a scenario can offer a deployment serving a *different* model
	// and catch one being substituted for the model the client named. With every
	// deployment serving the same id there was nothing to substitute one for, so
	// the promise could not be broken no matter what the routing did.
	Models []string
}

// modelIDs is what a deployment advertises.
func (d Deployment) modelIDs() []string {
	if len(d.Models) == 0 {
		return []string{"m"}
	}
	return d.Models
}

// Boolp is the scenario-facing way to state a capability, so a table of
// deployments can say what it means.
func Boolp(b bool) *bool { return &b }

// Scenario is one claim about routing, stated so it can fail.
type Scenario struct {
	Name string
	// Why states what a failure would mean for a user. It is required, because
	// a gate nobody can explain is a gate nobody will fix.
	Why string
	// Model is the alias the client asks for.
	Model string
	// Request is the client body.
	Request string
	// Deployments are the accounts available.
	Deployments []Deployment
	// Configure adjusts the proxy before the request.
	Configure func(*config.Config)
	// SeedUsage records calls before the request, so spend-dependent scenarios
	// exercise the real ledger rather than a stubbed window.
	SeedUsage []usage.Event
	// WantAccount is the account that must serve it, or "" for "any".
	WantAccount string
	// WantFailure requires the request to be refused.
	WantFailure bool
	// WantContains requires the upstream request to contain this.
	WantContains string
	// WantAbsent requires the upstream request not to contain this.
	WantAbsent string
}

// Result is one scenario's outcome.
type Result struct {
	Name    string
	Why     string
	Passed  bool
	Detail  string
	Account string
	Cost    float64
}

// Run executes a scenario against mock upstreams and reports what happened.
//
// Every provider is a local stub with a known price, so a scenario's cost is
// arithmetic rather than an estimate. That is the point: a gate that depends on
// a live provider's pricing is a gate that fails for reasons unrelated to the
// code under test.
func Run(t *testing.T, s Scenario) Result {
	t.Helper()

	type stub struct {
		id  string
		srv *httptest.Server
	}
	var stubs []stub
	var providers []config.Provider

	for i, d := range s.Deployments {
		d := d
		hits := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				var listed []map[string]string
				for _, id := range d.modelIDs() {
					listed = append(listed, map[string]string{"id": id})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"object": "list", "data": listed,
				})
				return
			}
			body, _ := readAll(r)
			seen = append(seen, seenReq{account: d.ID, body: string(body)})
			hits++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "answer from " + d.ID}}},
				"usage": map[string]int{
					"prompt_tokens": 1000, "completion_tokens": 500,
				},
			})
		}))
		t.Cleanup(srv.Close)
		stubs = append(stubs, stub{id: d.ID, srv: srv})

		tier := "paid"
		if d.Free {
			tier = "free"
		}
		// The stub is credentialed. Automatic routes refuse a provider with no
		// account attached unless the scenario opts in, and a scenario about
		// price or capability is not a scenario about that guard.
		providers = append(providers, config.Provider{
			ID: d.ID, Adapter: "openai_compat", Tier: tier, BaseURL: srv.URL + "/v1",
			APIKey: "sk-eval",
			Capabilities: config.ProviderCapabilities{
				Tools: d.Tools,
			},
		})
		_ = i
	}

	seen = nil
	cfg := config.Config{
		SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317,
		Providers: providers,
		AutomaticRoutes: config.AutomaticRoutePrefs{
			Enabled: true,
			Prices:  map[string]config.PriceQuote{},
		},
	}
	for _, d := range s.Deployments {
		in, out := d.Input, d.Output
		for _, id := range d.modelIDs() {
			cfg.AutomaticRoutes.Prices[d.ID+"/"+id] = config.PriceQuote{
				Input: &in, Output: &out, Verified: true,
			}
		}
	}
	if s.Configure != nil {
		s.Configure(&cfg)
	}

	gw, err := gateway.New(cfg, "", defaultRegistry())
	if err != nil {
		t.Fatalf("%s: gateway: %v", s.Name, err)
	}
	gw.SetUsage(usage.Open(""))
	// The session store the carried-context path needs. Without it
	// contextOptimize returns before it does anything, so a scenario about
	// tools surviving routing would never reach the code that could strip them.
	gw.Artifacts = contextstore.New(contextstore.Options{})
	for _, e := range s.SeedUsage {
		gw.Usage.Add(e)
	}
	gw.Refresh(context.Background())

	_, _, callErr := gw.Chat(context.Background(), []byte(s.Request))
	res := Result{Name: s.Name, Why: s.Why}

	switch {
	case s.WantFailure:
		if callErr == nil {
			res.Detail = "request succeeded but should have been refused"
			return res
		}
		// Refusing is the whole point of the scenario. Not reaching an upstream
		// is the desired outcome, not a failure to report.
		res.Passed = true
		res.Detail = callErr.Error()
		return res
	case callErr != nil:
		res.Detail = "request failed: " + callErr.Error()
		return res
	}

	if len(seen) == 0 {
		res.Detail = "no upstream received the request"
		return res
	}
	res.Account = seen[0].account
	res.Cost = costOf(s.Deployments, seen[0].account, 1000, 500)

	if s.WantAccount != "" && res.Account != s.WantAccount {
		res.Detail = fmt.Sprintf("served by %q, want %q", res.Account, s.WantAccount)
		return res
	}
	if s.WantContains != "" && !strings.Contains(seen[0].body, s.WantContains) {
		res.Detail = "upstream request is missing " + s.WantContains
		return res
	}
	if s.WantAbsent != "" && strings.Contains(seen[0].body, s.WantAbsent) {
		res.Detail = "upstream request unexpectedly contains " + s.WantAbsent
		return res
	}
	res.Passed = true
	return res
}

// seen captures upstream requests for the duration of one scenario.
var seen []seenReq

type seenReq struct {
	account string
	body    string
}

func readAll(r *http.Request) ([]byte, error) { return io.ReadAll(r.Body) }

func defaultRegistry() *adapter.Registry { return adapters.DefaultRegistry() }

func costOf(ds []Deployment, account string, input, output int) float64 {
	for _, d := range ds {
		if d.ID != account {
			continue
		}
		return (float64(input)*d.Input + float64(output)*d.Output) / 1e6
	}
	return 0
}

// RunAll executes scenarios and fails the test on the first broken promise,
// listing every result so a partial failure is diagnosable.
func RunAll(t *testing.T, scenarios []Scenario) {
	t.Helper()
	var results []Result
	for _, s := range scenarios {
		// As subtests, so a single promise can be selected by name from
		// outside -- which is what the mutation gate does when it re-runs one
		// scenario against a deliberately broken build.
		t.Run(s.Name, func(t *testing.T) {
			r := Run(t, s)
			status := "PASS"
			if !r.Passed {
				status = "FAIL"
			}
			t.Logf("%s  %-46s account=%-10s cost=$%.6f", status, r.Name, r.Account, r.Cost)
			results = append(results, r)
		})
	}
	sort.SliceStable(results, func(i, j int) bool { return !results[i].Passed && results[j].Passed })

	var broken []Result
	for _, r := range results {
		if !r.Passed {
			broken = append(broken, r)
		}
	}
	if len(broken) > 0 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("%d routing promise(s) broken:\n", len(broken)))
		for _, r := range broken {
			b.WriteString(fmt.Sprintf("  - %s: %s\n    why: %s\n", r.Name, r.Detail, r.Why))
		}
		t.Fatal(b.String())
	}
}
