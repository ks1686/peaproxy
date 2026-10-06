package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func adminGET(t *testing.T, s *Server, path string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Peaproxy-Admin", "test")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s -> %d %s", path, rr.Code, rr.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}

// The whole point of v3 is that it changes behaviour without being asked. A
// user who cannot see what is on cannot turn it off, which makes the
// opinionated default an unanswerable default.
func TestAdminPolicyReportsWhatIsActuallyOn(t *testing.T) {
	s, _ := testServer(t)
	body := adminGET(t, s, "/admin/policy")
	policy, ok := body["policy"].(map[string]any)
	if !ok {
		t.Fatalf("no policy object: %v", body)
	}
	for _, key := range []string{
		"automatic", "freeOnly", "contextOptimization",
		"spendCeilingUSD", "promptCache", "localAssistant",
	} {
		if _, present := policy[key]; !present {
			t.Errorf("policy is missing %q", key)
		}
	}
}

// Every value reported must be the resolved one, not the raw nullable. "unset"
// is an internal distinction; what a user needs to know is whether it is on.
func TestAdminPolicyReportsResolvedBooleans(t *testing.T) {
	s, _ := testServer(t)
	policy := adminGET(t, s, "/admin/policy")["policy"].(map[string]any)
	for _, key := range []string{"automatic", "freeOnly", "contextOptimization", "localAssistant"} {
		v, ok := policy[key].(bool)
		if !ok {
			t.Errorf("%s is %T, want a resolved bool", key, policy[key])
			continue
		}
		if key == "localAssistant" && v {
			t.Error("localAssistant reported on by default; it must be opt-in")
		}
	}
}

// With no ceiling configured the panel must say so rather than reporting zero,
// which would read as "you have spent nothing and the budget is gone".
func TestAdminPolicyDistinguishesNoCeilingFromZeroSpend(t *testing.T) {
	s, _ := testServer(t)
	policy := adminGET(t, s, "/admin/policy")["policy"].(map[string]any)

	ceiling, ok := policy["spendCeilingUSD"]
	if !ok {
		t.Fatal("no spendCeilingUSD reported")
	}
	if ceiling != nil {
		t.Fatalf("spendCeilingUSD = %v with none configured; want null", ceiling)
	}
	spend, ok := policy["spentLast30DaysUSD"].(float64)
	if !ok {
		t.Fatalf("spentLast30DaysUSD is %T, want a number", policy["spentLast30DaysUSD"])
	}
	if spend != 0 {
		t.Fatalf("spent = %v on a fresh proxy, want 0", spend)
	}
}

// The honesty rules apply to this panel like any other. A total PeaProxy
// cannot measure must not be presented as a complete one.
func TestAdminPolicyFlagsUnmeasurableSpend(t *testing.T) {
	s, _ := testServer(t)
	policy := adminGET(t, s, "/admin/policy")["policy"].(map[string]any)

	if _, present := policy["spendMeasurable"]; !present {
		t.Fatal("policy does not say whether spend could be measured")
	}
	note, _ := policy["spendNote"].(string)
	if note == "" {
		t.Fatal("spendNote is empty; the panel must explain what it is reporting")
	}
}

// The response must never leak configuration detail that includes a secret.
func TestAdminPolicyCarriesNoSecrets(t *testing.T) {
	s, _ := testServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/policy", nil)
	req.Header.Set("X-Peaproxy-Admin", "test")
	s.Handler().ServeHTTP(rr, req)
	body := rr.Body.String()
	for _, bad := range []string{"apiKey", "api_key", "Bearer", "sk-", "password"} {
		if strings.Contains(body, bad) {
			t.Errorf("policy response contains %q", bad)
		}
	}
}
