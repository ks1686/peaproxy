package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `optimization status` and `optimization explain` exist because the policy
// panel is in a browser and the person debugging a routing decision is at a
// terminal. The tests below run both against a fake upstream, so the answers
// come from the real router rather than a hand-written fixture.

func writeExplainConfig(t *testing.T, baseURL string, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	if body == "" {
		body = `schemaVersion: 1
bind: 127.0.0.1
port: 8399
providers:
  - id: key-one
    adapter: openai_compat
    tier: paid
    baseURL: ` + baseURL + `
    apiKeyEnv: PP_EXPLAIN_TEST_KEY
`
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PP_EXPLAIN_TEST_KEY", "dummy")
	return path
}

// modelsServer answers the /v1/models discovery the gateway performs, and
// refuses everything else: explain must not dispatch a request.
func modelsServer(t *testing.T, models ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("explain contacted %s; it must not dispatch anything", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		data := make([]map[string]string, 0, len(models))
		for _, m := range models {
			data = append(data, map[string]string{"id": m})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestExplainNamesTheAccountsThatServeAModel(t *testing.T) {
	srv := modelsServer(t, "upstream-model")
	path := writeExplainConfig(t, srv.URL+"/v1", "")

	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"optimization", "explain", "upstream-model", "--config", path}, out); err != nil {
		t.Fatalf("explain: %v\n%s", err, out)
	}
	text := out.String()
	if !strings.Contains(text, "key-one") {
		t.Errorf("explain did not name the serving account:\n%s", text)
	}
	if !strings.Contains(text, "eligible=true") {
		t.Errorf("explain did not mark a healthy account eligible:\n%s", text)
	}
}

func TestExplainSaysSoWhenNothingServesTheModel(t *testing.T) {
	srv := modelsServer(t, "upstream-model")
	path := writeExplainConfig(t, srv.URL+"/v1", "")

	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"optimization", "explain", "no-such-model", "--config", path}, out); err != nil {
		t.Fatalf("explain: %v\n%s", err, out)
	}
	text := out.String()
	// "candidates: none" alone would be indistinguishable from a crash, and a
	// bare refusal tells the user nothing they did not already know.
	if !strings.Contains(text, "no account serves") {
		t.Errorf("explain did not say why nothing was available:\n%s", text)
	}
}

func TestExplainMarksWhatItCannotKnowRatherThanGuessing(t *testing.T) {
	srv := modelsServer(t, "upstream-model")
	path := writeExplainConfig(t, srv.URL+"/v1", "")

	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"optimization", "explain", "pea/economy", "--config", path}, out); err != nil {
		t.Fatalf("explain: %v\n%s", err, out)
	}
	// An automatic route's order depends on price ranking and the request
	// session. Listing deployments without saying the order is not the try
	// order is exactly the confident wrong answer this command must not give.
	if !strings.Contains(out.String(), "incomplete:") {
		t.Errorf("explain did not mark the automatic-route answer as partial:\n%s", out.String())
	}
}

func TestExplainOfAnAutomaticRouteAppliesTheSameFiltersAsARequest(t *testing.T) {
	srv := modelsServer(t, "upstream-model")
	body := `schemaVersion: 1
bind: 127.0.0.1
port: 8399
providers:
  - id: key-one
    adapter: openai_compat
    tier: paid
    baseURL: ` + srv.URL + `/v1
    apiKeyEnv: PP_EXPLAIN_TEST_KEY
optimization:
  freeOnly: true
  allowAnonymousProviders: false
`
	path := writeExplainConfig(t, srv.URL+"/v1", body)
	// pea/auto, not pea/free: the route kind is checked first, exactly as the
	// request path checks it, so a paid deployment is already outside pea/free
	// and freeOnly would never be consulted for it. Using pea/free here would
	// assert the wrong reason.
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"optimization", "explain", "pea/auto", "--config", path}, out); err != nil {
		t.Fatalf("explain: %v\n%s", err, out)
	}
	text := out.String()
	if !strings.Contains(text, "freeOnly") {
		t.Errorf("explain did not apply the freeOnly filter a request would:\n%s", text)
	}
	if !strings.Contains(text, "nothing is eligible") {
		t.Errorf("explain did not reach the refusal a request would:\n%s", text)
	}
	if !strings.Contains(text, "key-one") {
		t.Errorf("explain dropped the account instead of naming why it is out:\n%s", text)
	}
}

func TestExplainAppliesTheRouteKindFilterBeforeAnythingElse(t *testing.T) {
	srv := modelsServer(t, "upstream-model")
	path := writeExplainConfig(t, srv.URL+"/v1", "")
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"optimization", "explain", "pea/local", "--config", path}, out); err != nil {
		t.Fatalf("explain: %v\n%s", err, out)
	}
	// A cloud account is not on-machine, so pea/local must say so rather than
	// listing it as a candidate.
	if !strings.Contains(out.String(), "kind") {
		t.Errorf("explain did not name the route-kind filter:\n%s", out.String())
	}
}

func TestStatusPrintsTheEffectivePolicyAndTheMeasurableSpend(t *testing.T) {
	srv := modelsServer(t, "upstream-model")
	path := writeExplainConfig(t, srv.URL+"/v1", `schemaVersion: 1
bind: 127.0.0.1
port: 8399
providers:
  - id: key-one
    adapter: openai_compat
    tier: paid
    baseURL: `+srv.URL+`/v1
    apiKeyEnv: PP_EXPLAIN_TEST_KEY
optimization:
  spendCeilingUSD: 25
  freeOnly: true
`)
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"optimization", "status", "--config", path}, out); err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	text := out.String()
	for _, want := range []string{
		"freeOnly: true",
		"spendCeilingUSD: 25.0000 USD",
		"spendMeasurable:",
		"pricedCallsLast30Days: 0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status does not report %q:\n%s", want, text)
		}
	}
}

func TestStatusNamesAnInertSettingBecauseThatIsWhySomeoneIsRunningIt(t *testing.T) {
	srv := modelsServer(t, "upstream-model")
	path := writeExplainConfig(t, srv.URL+"/v1", `schemaVersion: 1
bind: 127.0.0.1
port: 8399
providers:
  - id: key-one
    adapter: openai_compat
    tier: paid
    baseURL: `+srv.URL+`/v1
    apiKeyEnv: PP_EXPLAIN_TEST_KEY
optimization:
  persistentContext: true
`)
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"optimization", "status", "--config", path}, out); err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	// Someone running `status` because a policy "isn't working" is exactly the
	// person who needs to be told this one does nothing.
	if !strings.Contains(out.String(), "inert: optimization.persistentContext") {
		t.Errorf("status hid an inert setting:\n%s", out.String())
	}
}

func TestExplainRequiresAModelAndSaysWhichAccountItCannotFind(t *testing.T) {
	srv := modelsServer(t, "upstream-model")
	path := writeExplainConfig(t, srv.URL+"/v1", "")
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"optimization", "explain", "upstream-model", "--account", "no-such", "--config", path}, out)
	if err == nil || !strings.Contains(err.Error(), "no-such") {
		t.Fatalf("explain with an unknown account = %v\n%s", err, out)
	}
}
