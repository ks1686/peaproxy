package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthCountsListedModels(t *testing.T) {
	for _, tc := range []struct {
		name, hide string
		failed     bool
		want       int
	}{
		{name: "visible", want: 2},
		{name: "hidden model", hide: "hide:\n  models: [hidden-model]\n", want: 1},
		{name: "hidden provider", hide: "hide:\n  providers: [openai_compat]\n", want: 0},
		{name: "unavailable placeholder", failed: true, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var srv *httptest.Server
			if tc.failed {
				srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
				t.Cleanup(srv.Close)
			} else {
				srv = modelsServer(t, "visible-model", "hidden-model")
			}
			body := "schemaVersion: 1\nbind: 127.0.0.1\nport: 8399\nproviders:\n  - id: key-one\n    adapter: openai_compat\n    tier: paid\n    baseURL: " + srv.URL + "/v1\n    apiKeyEnv: PP_EXPLAIN_TEST_KEY\n" + tc.hide
			path := writeExplainConfig(t, srv.URL+"/v1", body)
			var out bytes.Buffer
			if err := ExecuteWithArgs([]string{"health", "--config", path}, &out); err != nil {
				t.Fatal(err)
			}
			if want := fmt.Sprintf("\nmodels: %d\n", tc.want); !strings.Contains(out.String(), want) {
				t.Fatalf("want %q in health:\n%s", want, out.String())
			}
		})
	}
}
