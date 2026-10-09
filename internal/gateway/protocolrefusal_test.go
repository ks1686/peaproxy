package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
)

// #83: a model the provider refuses to serve on the chat wire was still
// advertised in /v1/models, so every harness offered it and every call failed
// upstream. The provider already says so explicitly:
//
//	{"type":"error","error":{"type":"ModelProtocolUnsupported",
//	 "message":"Model does not support this protocol."}}
//
// So the fix is to believe the provider rather than guess from the model name:
// remember the refusal, stop advertising the model for chat, and do not cool
// the account down for a refusal that says nothing about the account.

const protocolUnsupported = `{"type":"error","error":{"type":"ModelProtocolUnsupported","message":"Model does not support this protocol."}}`

func refusesModel(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/models" {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "muse-spark"}}})
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(protocolUnsupported))
}

func zenGateway(t *testing.T) *Gateway {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(refusesModel))
	t.Cleanup(srv.Close)
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Providers: []config.Provider{
			{ID: "opencode-zen", Adapter: "openai_compat", Tier: "freemium", BaseURL: srv.URL + "/v1"},
		},
		Failover: config.FailoverPrefs{Policy: "fill-first"},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	return gw
}

func listedIDs(t *testing.T, gw *Gateway, forClients bool) []string {
	t.Helper()
	var out []string
	for _, m := range gw.Listed(catalog.FilterAll) {
		if !forClients || m.Exposed {
			out = append(out, m.ID)
		}
	}
	return out
}

// The model is offered before anyone has called it -- that is the report.
func TestModelRefusingTheChatProtocolIsAdvertisedUntilItRefusesOnce(t *testing.T) {
	gw := zenGateway(t)
	if !contains2(listedIDs(t, gw, true), "muse-spark") {
		t.Fatal("fixture is wrong: the model should start out advertised")
	}
}

// After the refusal it must stop being offered for chat.
func TestModelRefusingTheChatProtocolStopsBeingListed(t *testing.T) {
	gw := zenGateway(t)
	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"muse-spark"}`)); err == nil {
		t.Fatal("expected the refusal to surface")
	}
	if contains2(listedIDs(t, gw, true), "muse-spark") {
		t.Errorf("still advertised to clients after the provider refused it: %v", listedIDs(t, gw, true))
	}
}

// It must still be visible to the admin, with a reason. Silently deleting a
// model from the catalog would leave the user with no way to find out why it
// disappeared.
func TestModelRefusingTheChatProtocolStaysVisibleToAdmin(t *testing.T) {
	gw := zenGateway(t)
	_, _, _ = gw.Chat(context.Background(), []byte(`{"model":"muse-spark"}`))
	// Annotated is the admin view (it includes hidden rows); Listed is what
	// clients get.
	var found bool
	for _, m := range gw.Annotated(catalog.FilterAll) {
		if m.ID != "muse-spark" {
			continue
		}
		found = true
		if !strings.Contains(m.Status, "not_chat") {
			t.Errorf("admin row has no reason on it: status=%q", m.Status)
		}
		if m.Exposed || m.Routable {
			t.Errorf("admin row is still offered for chat: exposed=%v routable=%v", m.Exposed, m.Routable)
		}
	}
	if !found {
		t.Error("the model vanished from the admin catalog entirely")
	}
}

// A refusal says the model cannot serve this protocol. It says nothing about
// the account, so cooling the account would take a healthy one out of rotation
// for the whole cooldown.
func TestModelRefusalDoesNotCoolDownTheAccount(t *testing.T) {
	gw := zenGateway(t)
	_, _, _ = gw.Chat(context.Background(), []byte(`{"model":"muse-spark"}`))
	for _, cd := range gw.Cooldowns() {
		if cd.AccountID == "opencode-zen" {
			t.Errorf("a model-level refusal cooled the account down: %+v", cd)
		}
	}
}

// The second call must not go to the upstream at all: the answer is already
// known, and a repeated refusal is a repeated 400 for the harness.
func TestSecondCallToARefusingModelDoesNotReachTheUpstream(t *testing.T) {
	var chatCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "muse-spark"}}})
			return
		}
		chatCalls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(protocolUnsupported))
	}))
	t.Cleanup(srv.Close)
	cfg := config.Config{
		SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317,
		Failover: config.FailoverPrefs{Policy: "fill-first"},
		Providers: []config.Provider{
			{ID: "opencode-zen", Adapter: "openai_compat", Tier: "freemium", BaseURL: srv.URL + "/v1"},
		},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())

	for i := 0; i < 3; i++ {
		if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"muse-spark"}`)); err == nil {
			t.Fatalf("call %d unexpectedly succeeded", i+1)
		}
	}
	if chatCalls != 1 {
		t.Errorf("the upstream was called %d times for a model already known to refuse", chatCalls)
	}
}

// A plain 400 is not a protocol refusal and must not remove anything: providers
// return 400 for a malformed request too.
func TestPlainBadRequestIsNotAProtocolRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid_request_error: messages is required"}}`))
	}))
	t.Cleanup(srv.Close)
	cfg := config.Config{
		SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317,
		Providers: []config.Provider{
			{ID: "acct-a", Adapter: "openai_compat", Tier: "paid", BaseURL: srv.URL + "/v1"},
		},
	}
	gw, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())

	for i := 0; i < 2; i++ {
		if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"m"}`)); err == nil {
			t.Fatal("expected the 400 to surface")
		}
	}
	for _, m := range gw.Annotated(catalog.FilterAll) {
		if m.ID == "m" && strings.Contains(m.Status, "not_chat") {
			t.Errorf("a plain 400 was read as a protocol refusal: status=%q", m.Status)
		}
	}
}

func contains2(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// #136: Copilot refuses a Responses-only model on chat with its own code. That
// is a protocol refusal, so it must be believed like ModelProtocolUnsupported,
// not read as a malformed request.
func TestCopilotUnsupportedAPIForModelIsAProtocolRefusal(t *testing.T) {
	err := adapter.HTTPError{
		Status: http.StatusBadRequest,
		Body:   `{"error":{"message":"model \"mai-code-1.1-flash\" is not accessible via the /chat/completions endpoint","code":"unsupported_api_for_model"}}`,
	}
	if !protocolRefused(err) {
		t.Fatalf("unsupported_api_for_model must count as a protocol refusal")
	}
}
