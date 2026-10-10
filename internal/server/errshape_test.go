package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
)

// failingServer builds a server whose only upstream always answers status with
// body. Anything the adapter wraps from that response must never reach a client
// verbatim.
func failingServer(t *testing.T, status int, body string, hdr map[string]string) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data":   []map[string]string{{"id": "m"}},
			})
			return
		}
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(up.Close)
	cfg := config.Config{
		SchemaVersion: 1,
		Bind:          "127.0.0.1",
		Port:          8317,
		Providers: []config.Provider{{
			ID:      "local",
			Adapter: "openai_compat",
			Tier:    "local",
			BaseURL: up.URL + "/v1",
		}},
	}
	gw, err := gateway.New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	front := httptest.NewServer(New(Options{Gateway: gw}).Handler())
	t.Cleanup(front.Close)
	return front
}

func postJSON(t *testing.T, front *httptest.Server, path, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, front.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, raw
}

// #70: the upstream body is not the client's business. It can echo request
// fragments or credentials back at us.
func TestClientErrorsOmitUpstreamBody(t *testing.T) {
	const secret = "sk-live-DO-NOT-LEAK-user@example.com prompt=my private prompt"
	routes := []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"m","input":"hi"}`},
	}
	for _, tc := range routes {
		t.Run(tc.path, func(t *testing.T) {
			front := failingServer(t, http.StatusBadRequest,
				`{"error":{"message":"`+secret+`","type":"invalid_request_error"}}`, nil)
			resp, raw := postJSON(t, front, tc.path, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, raw)
			}
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("upstream body leaked to the client: %s", raw)
			}
			if bytes.Contains(raw, []byte("sk-live-")) || bytes.Contains(raw, []byte("my private prompt")) {
				t.Fatalf("upstream fragment leaked to the client: %s", raw)
			}
			// The status still identifies the upstream failure.
			if !bytes.Contains(raw, []byte("400")) {
				t.Fatalf("error lost the upstream status: %s", raw)
			}
		})
	}
}

// #57: OpenAI SDKs read error.type and error.message, not a bare string.
func TestOpenAIWireErrorEnvelope(t *testing.T) {
	front := failingServer(t, http.StatusBadRequest, `{"error":{"message":"nope"}}`, nil)
	resp, raw := postJSON(t, front, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var got struct {
		Error struct {
			Message string  `json:"message"`
			Type    string  `json:"type"`
			Param   *string `json:"param"`
			Code    *string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if got.Error.Message == "" {
		t.Fatalf("error.message empty: %s", raw)
	}
	if got.Error.Type != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error: %s", got.Error.Type, raw)
	}
	// Both keys must be present and null; SDKs read them unconditionally.
	for _, key := range []string{`"param":null`, `"code":null`} {
		if !bytes.Contains(raw, []byte(key)) {
			t.Fatalf("OpenAI envelope missing %s: %s", key, raw)
		}
	}
}

// #57: Anthropic SDKs look at the top-level type plus error.type.
func TestAnthropicWireErrorEnvelope(t *testing.T) {
	front := failingServer(t, http.StatusBadRequest, `{"error":{"message":"nope"}}`, nil)
	resp, raw := postJSON(t, front, "/v1/messages", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var got struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if got.Type != "error" {
		t.Fatalf("top-level type = %q, want \"error\": %s", got.Type, raw)
	}
	if got.Error.Type != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error: %s", got.Error.Type, raw)
	}
	if got.Error.Message == "" {
		t.Fatalf("error.message empty: %s", raw)
	}
}

// #57: Responses is an OpenAI wire, not an Anthropic one.
func TestResponsesWireUsesOpenAIEnvelope(t *testing.T) {
	front := failingServer(t, http.StatusBadRequest, `{"error":{"message":"nope"}}`, nil)
	_, raw := postJSON(t, front, "/v1/responses", `{"model":"m","input":"hi"}`)
	if !bytes.Contains(raw, []byte(`"error":{`)) {
		t.Fatalf("responses should use the OpenAI object envelope: %s", raw)
	}
	if bytes.Contains(raw, []byte(`"type":"error"`)) {
		t.Fatalf("responses must not use the Anthropic envelope: %s", raw)
	}
}

// #57: admin handlers keep the plain string form. #70 still applies to them:
// /admin/* is reachable from a browser, and #75 notes the reads skip the
// loopback Host check, so the body is sanitized everywhere it is written.
func TestAdminErrorKeepsStringFormAndOmitsBody(t *testing.T) {
	const secret = "sk-live-admin-must-not-see-this"
	front := failingServer(t, http.StatusBadRequest, `{"error":{"message":"`+secret+`"}}`, nil)
	_, raw := postJSON(t, front, "/admin/showcase", `{"model":"m","imageOut":true,"prompt":"x"}`)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	msg, ok := got["error"].(string)
	if !ok {
		t.Fatalf("admin error should stay a string, got %T: %s", got["error"], raw)
	}
	if strings.Contains(msg, secret) {
		t.Fatalf("admin error leaked the upstream body: %s", raw)
	}
}

// #57: pre-routing middleware errors are not provider errors and keep the
// string form on both wires.
func TestMiddlewareErrorKeepsStringForm(t *testing.T) {
	front := failingServer(t, http.StatusBadRequest, `{"error":{"message":"nope"}}`, nil)
	req, err := http.NewRequest(http.MethodPost, front.URL+"/v1/chat/completions",
		strings.NewReader("model=m"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415: %s", resp.StatusCode, raw)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if _, ok := got["error"].(string); !ok {
		t.Fatalf("middleware error should stay a string, got %T: %s", got["error"], raw)
	}
}

// #57: the type is derived from the status so SDKs can classify it.
func TestErrorTypeForStatus(t *testing.T) {
	cases := []struct {
		status         int
		openAI, anthro string
	}{
		{http.StatusBadRequest, "invalid_request_error", "invalid_request_error"},
		{http.StatusUnauthorized, "authentication_error", "authentication_error"},
		{http.StatusForbidden, "permission_error", "permission_error"},
		{http.StatusNotFound, "not_found_error", "not_found_error"},
		{http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large"},
		{http.StatusTooManyRequests, "rate_limit_error", "rate_limit_error"},
		{http.StatusBadGateway, "server_error", "api_error"},
		{http.StatusServiceUnavailable, "server_error", "overloaded_error"},
		{http.StatusInternalServerError, "server_error", "api_error"},
	}
	for _, tc := range cases {
		if got := errorTypeFor(wireOpenAI, tc.status); got != tc.openAI {
			t.Errorf("openai %d = %q, want %q", tc.status, got, tc.openAI)
		}
		if got := errorTypeFor(wireAnthropic, tc.status); got != tc.anthro {
			t.Errorf("anthropic %d = %q, want %q", tc.status, got, tc.anthro)
		}
	}
}

// #57 says keep Retry-After. A non-cooldown status carries the header straight
// through writeErr.
func TestRetryAfterHeaderPreserved(t *testing.T) {
	front := failingServer(t, http.StatusBadRequest, `{"error":{"message":"slow down"}}`,
		map[string]string{"Retry-After": "42"})
	resp, raw := postJSON(t, front, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Retry-After"); got != "42" {
		t.Fatalf("Retry-After = %q, want 42: %s", got, raw)
	}
}

// A 429 upstream puts the only account in cooldown, so the client gets the
// documented 503 + Retry-After. The message must still not carry the body, and
// it must survive the CooldownError wrapper.
func TestCooldownReachesClientAs503Sanitized(t *testing.T) {
	const secret = "sk-live-cooldown-must-not-leak"
	front := failingServer(t, http.StatusTooManyRequests, `{"error":{"message":"`+secret+`"}}`,
		map[string]string{"Retry-After": "30"})
	resp, raw := postJSON(t, front, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 once the account is cooling: %s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Fatalf("cooldown must still tell the client when to come back: %s", raw)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatalf("cooldown wrapper leaked the upstream body: %s", raw)
	}
	if !bytes.Contains(raw, []byte("all matching accounts in cooldown")) {
		t.Fatalf("cooldown context lost: %s", raw)
	}
	if !bytes.Contains(raw, []byte("429")) {
		t.Fatalf("cooldown message should still name the upstream status: %s", raw)
	}
}

// A streaming client sees the refusal before any SSE byte. The message has to
// name the protocol, and the OpenAI code has to be the provider's code, so a
// harness can tell a protocol mismatch from a bad prompt. The upstream body
// still stays out.
func TestStreamProtocolRefusalNamesTheCause(t *testing.T) {
	const secret = "sk-live-DO-NOT-LEAK"
	body := `{"error":{"message":"model \"grok-4.7\" is not accessible via the /chat/completions endpoint","code":"unsupported_api_for_model","api_key":"` + secret + `"}}`
	front := failingServer(t, http.StatusBadRequest, body, nil)
	resp, raw := postJSON(t, front, "/v1/chat/completions", `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("stream failure was reported as 200: %s", raw)
	}
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte("grok-4.7")) {
		t.Fatalf("upstream body leaked: %s", raw)
	}
	if bytes.Contains(raw, []byte("upstream HTTP 400")) {
		t.Fatalf("opaque 400 reached the client: %s", raw)
	}
	if !bytes.Contains(raw, []byte("does not serve that model on the chat protocol")) {
		t.Fatalf("refusal not named: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"code":"unsupported_api_for_model"`)) {
		t.Fatalf("refusal code not sourced: %s", raw)
	}
}

// A 400 whose body is a usage limit must not look like a bad request. The
// client gets a rate-limit type and a stable code, without the provider body.
func TestUsageLimit400IsNamedForTheClient(t *testing.T) {
	const secret = "sk-live-DO-NOT-LEAK"
	body := `{"error":{"message":"You have reached your usage limit","type":"invalid_request_error","api_key":"` + secret + `"}}`
	front := failingServer(t, http.StatusBadRequest, body, nil)
	resp, raw := postJSON(t, front, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("usage limit was reported as 200: %s", raw)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatalf("upstream body leaked: %s", raw)
	}
	if !bytes.Contains(raw, []byte("usage limit")) {
		t.Fatalf("usage limit not named: %s", raw)
	}
	if !bytes.Contains(raw, []byte("rate_limit_error")) {
		t.Fatalf("usage limit classified as a bad request: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"code":"usage_limit"`)) {
		t.Fatalf("usage limit code not sourced: %s", raw)
	}
	if !bytes.Contains(raw, []byte("400")) {
		t.Fatalf("upstream status dropped: %s", raw)
	}
}

// #136: a model refused for its protocol must reach the client as a named
// refusal, not the opaque "upstream HTTP 400". The upstream body still stays
// out of the response.
func TestProtocolRefusalReachesClientNamed(t *testing.T) {
	const body = `{"error":{"message":"model \"grok-4.7\" is not accessible via the /chat/completions endpoint","code":"unsupported_api_for_model"}}`
	front := failingServer(t, http.StatusBadRequest, body, nil)
	_, raw := postJSON(t, front, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if bytes.Contains(raw, []byte("grok-4.7")) {
		t.Fatalf("upstream body leaked: %s", raw)
	}
	if !bytes.Contains(raw, []byte("does not serve that model on the chat protocol")) {
		t.Fatalf("refusal not named to the client: %s", raw)
	}
}
