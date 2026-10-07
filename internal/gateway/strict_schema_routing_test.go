package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/requestmeta"
)

// Issue #126. v3.0.4 required positive evidence before routing a capability, and
// applied that rule to exact-model routing despite a comment promising it stayed
// permissive. No evidence for strict_schema can exist: no model-listing API
// reports it and no compat profile carries the field, so every deployment was
// excluded and every strict-tools request was refused with advice to configure a
// field the configuration does not have.

const strictToolsBody = `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],` +
	`"tools":[{"type":"function","strict":true,"function":{"name":"lookup"}}]}`

// unnamedGateway serves one model and declares nothing about its capabilities --
// the ordinary case, and the one that broke.
func unnamedGateway(t *testing.T) (*Gateway, func() int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "gpt-5"}}})
			return
		}
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317,
		Providers: []config.Provider{{
			ID: "quiet", Adapter: "openai_compat", Tier: "paid", BaseURL: srv.URL + "/v1",
			APIKey: "sk-test",
		}},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	return gw, func() int { return calls }
}

// The regression itself: a strict tool call against a deployment that has simply
// never been asked about strict schemas.
func TestAStrictToolCallRoutesToADeploymentNobodyHasDescribed(t *testing.T) {
	gw, calls := unnamedGateway(t)

	_, _, err := gw.Chat(requestmeta.WithRequest(context.Background(), requestmeta.Request{
		Wire:         requestmeta.WireChat,
		Requirements: requestmeta.RequirementsFromBody(requestmeta.WireChat, []byte(strictToolsBody)),
	}), []byte(strictToolsBody))

	if err != nil {
		t.Fatalf("a strict tool call was refused by a deployment that was never told it cannot do it: %v", err)
	}
	if calls() != 1 {
		t.Fatalf("the request was not served (%d calls)", calls())
	}
}

// Same shape on the automatic path, which used the same rule.
func TestAStrictToolCallStillRoutesWhenTheRouteIsChosenAutomatically(t *testing.T) {
	gw, calls := unnamedGateway(t)
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.cfg.AutomaticRoutes.Auto = []string{"gpt-5"}

	_, _, err := gw.Chat(requestmeta.WithRequest(context.Background(), requestmeta.Request{
		Wire:         requestmeta.WireChat,
		Requirements: requestmeta.RequirementsFromBody(requestmeta.WireChat, []byte(strictToolsBody)),
	}), []byte(`{"model":"pea/auto","messages":[{"role":"user","content":"hi"}],`+
		`"tools":[{"type":"function","strict":true,"function":{"name":"lookup"}}]}`))

	if err != nil {
		t.Fatalf("automatic routing refused a strict tool call on unknown evidence: %v", err)
	}
	if calls() != 1 {
		t.Fatalf("the request was not served (%d calls)", calls())
	}
}

// The guard v3.0.4 was for must still hold: a provider told it cannot do tools
// is still refused them. "Nobody said" is not "said no".
func TestAStatedRefusalIsStillRefusedAfterRelaxingUnknownEvidence(t *testing.T) {
	gw, upstream := toollessGateway(t)

	_, _, err := gw.Chat(requestmeta.WithRequest(context.Background(), requestmeta.Request{
		Wire:         requestmeta.WireChat,
		Requirements: requestmeta.RequirementsFromBody(requestmeta.WireChat, []byte(withToolsBody)),
	}), []byte(withToolsBody))

	if err == nil {
		t.Fatal("a tools request was served by a deployment declared tools: false")
	}
	if upstream() != 0 {
		t.Fatalf("the request reached the endpoint anyway (%d calls)", upstream())
	}
}

// A refusal that cannot be acted on is a dead end. v3.0.4's message said to
// declare capabilities under a provider, which for strict_schema names a field
// the configuration does not have. Refusals now name the providers responsible.
func TestARefusalNamesTheProvidersThatCausedIt(t *testing.T) {
	gw, _ := toollessGateway(t)

	_, _, err := gw.Chat(requestmeta.WithRequest(context.Background(), requestmeta.Request{
		Wire:         requestmeta.WireChat,
		Requirements: requestmeta.RequirementsFromBody(requestmeta.WireChat, []byte(withToolsBody)),
	}), []byte(withToolsBody))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "toolless") {
		t.Fatalf("the refusal does not name the deployment responsible: %v", err)
	}
}

// The exact-model path used to read capabilities from request context only, so
// any caller that reached Chat without requestmeta set skipped the filter
// entirely while appearing to have run it. The eval harness is one such caller;
// anything embedding the gateway is another.
func TestCapabilityFilteringSurvivesACallerThatSetsNoRequestMeta(t *testing.T) {
	gw, upstream := toollessGateway(t)

	_, _, err := gw.Chat(context.Background(), []byte(withToolsBody))
	if err == nil {
		t.Fatal("a tools request was served by a deployment declared tools: false, " +
			"because the caller did not set request metadata")
	}
	if upstream() != 0 {
		t.Fatalf("the request reached the endpoint anyway (%d calls)", upstream())
	}
}
