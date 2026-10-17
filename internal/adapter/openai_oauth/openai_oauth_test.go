package openai_oauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/oauth"
)

func TestAuthURLIncludesCodexClientAndPKCE(t *testing.T) {
	pkce := oauth.PKCE{Verifier: "v", Challenge: "chal"}
	u, err := url.Parse(AuthorizeURL(pkce, "st-9"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "auth.openai.com" || u.Path != "/oauth/authorize" {
		t.Fatalf("%s %s", u.Host, u.Path)
	}
	q := u.Query()
	if q.Get("client_id") != ClientID {
		t.Fatalf("client_id %s", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != RedirectURI {
		t.Fatalf("redirect %s", q.Get("redirect_uri"))
	}
	if q.Get("code_challenge") != "chal" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("%v", q)
	}
	if q.Get("codex_cli_simplified_flow") != "true" {
		t.Fatalf("missing simplified flow: %v", q)
	}
	if !strings.Contains(q.Get("scope"), "offline_access") {
		t.Fatalf("scope %s", q.Get("scope"))
	}
}

func TestExchangeIsFormEncodedAndParsesJWTAccount(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"email": "c@d.e",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct_99",
			"chatgpt_plan_type":  "plus",
		},
	})
	idTok := "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(raw))
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("content-type %s", r.Header.Get("Content-Type"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "codex-at",
			"refresh_token": "codex-rt",
			"id_token":      idTok,
			"expires_in":    3600,
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.pending = &pendingAuth{pkce: oauth.PKCE{Verifier: "ver"}, state: "st"}
	if err := a.AuthComplete(context.Background(), adapter.AuthSession{State: "st"}, "code-x"); err != nil {
		t.Fatal(err)
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("code_verifier") != "ver" {
		t.Fatalf("%v", form)
	}
	if a.token.AccessToken != "codex-at" || a.token.AccountID != "acct_99" || a.token.Email != "c@d.e" {
		t.Fatalf("%#v", a.token)
	}
	a.pending = &pendingAuth{pkce: oauth.PKCE{Verifier: "ver"}, state: "st"}
	if err := a.AuthComplete(context.Background(), adapter.AuthSession{State: "st"}, "http://127.0.0.1/cb?code=nope&state=wrong"); err == nil {
		t.Fatal("mismatched oauth state was accepted")
	}
}

func TestListModelsSendsCodexClientVersionQuery(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "gpt-5"}},
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct_99", ExpiresAt: time.Now().Add(time.Hour)}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 {
		t.Fatalf("%v %#v", err, models)
	}
	// Official Codex CLI GETs /models?client_version=<cargo version> (see
	// openai/codex ModelsClient::append_client_version_query). chatgpt.com
	// returns 400 "Field required: query client_version" without it.
	want := strings.TrimPrefix(UserAgent, Originator+"/")
	if got := gotQuery.Get("client_version"); got != want {
		t.Fatalf("client_version=%q want %q; query=%v", got, want, gotQuery)
	}
}

func TestChatPostsResponsesWithBearerAndAccountHeader(t *testing.T) {
	var gotPath, gotAuth, gotAcct, gotOrig string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAcct = r.Header.Get("Chatgpt-Account-Id")
		gotOrig = r.Header.Get("Originator")
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "gpt-5"}},
			})
		case strings.HasSuffix(r.URL.Path, "/responses"):
			body, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    "resp_1",
				"model": "gpt-5",
				"output": []map[string]any{{
					"type": "message",
					"content": []map[string]string{{
						"type": "output_text", "text": "hello codex",
					}},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct_99", ExpiresAt: time.Now().Add(time.Hour)}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 || !models[0].SubscriptionOAuth {
		t.Fatalf("%v %#v", err, models)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "gpt-5",
		Raw:   []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil || resp.Content != "hello codex" {
		t.Fatalf("%v %#v", err, resp)
	}
	if !strings.HasSuffix(gotPath, "/responses") {
		t.Fatalf("path %s", gotPath)
	}
	if gotAuth != "Bearer tok" || gotAcct != "acct_99" {
		t.Fatalf("auth=%q acct=%q", gotAuth, gotAcct)
	}
	if gotOrig == "" {
		t.Fatal("missing Originator")
	}
	if bytes.Contains(body, []byte(`"messages"`)) {
		t.Fatalf("should send Responses input, got %s", body)
	}
	if !bytes.Contains(body, []byte(`"input"`)) {
		t.Fatalf("missing input: %s", body)
	}
	assertCodexResponsesBody(t, body, true)
}

func TestResponsesPassthroughKeepsInput(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          "resp_native",
			"object":      "response",
			"output_text": "native",
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct_99", ExpiresAt: time.Now().Add(time.Hour)}
	raw := []byte(`{"model":"gpt-5","input":"codex ping"}`)
	out, err := a.Responses(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(gotBody, []byte(`"input"`)) || bytes.Contains(gotBody, []byte(`"messages"`)) {
		t.Fatalf("passthrough body %s", gotBody)
	}
	if !bytes.Contains(out, []byte(`"output_text":"native"`)) {
		t.Fatalf("native response %s", out)
	}
}

func TestResponsesDropsStreamOptionsForCodexOAuth(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          "resp_native",
			"object":      "response",
			"output_text": "ok",
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct_99", ExpiresAt: time.Now().Add(time.Hour)}
	cases := []struct {
		name string
		raw  string
		keep []string
	}{
		{
			name: "top-level stream_options dropped",
			raw:  `{"model":"gpt-5","input":"codex ping","stream_options":{"include_usage":true}}`,
			keep: []string{`"text":"codex ping"`, `"input_text"`},
		},
		{
			name: "quoted stream_options in input kept",
			raw:  `{"model":"gpt-5","input":"mention stream_options please","stream_options":{"include_usage":true},"max_output_tokens":16}`,
			keep: []string{`"text":"mention stream_options please"`, `"input_text"`},
		},
		{
			name: "nested stream_options in tools kept",
			raw:  `{"model":"gpt-5","tools":[{"type":"function","name":"lookup","parameters":{"stream_options":true}}],"input":"x","stream_options":{"include_usage":true}}`,
			keep: []string{`"parameters":{"stream_options":true}`, `"name":"lookup"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBody = nil
			if _, err := a.Responses(context.Background(), []byte(tc.raw)); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(gotBody, []byte(`"stream_options":{"include_usage":true}`)) {
				t.Fatalf("Codex OAuth must drop top-level stream_options: %s", gotBody)
			}
			if bytes.Contains(gotBody, []byte(`"max_output_tokens"`)) {
				t.Fatalf("Codex OAuth must omit max_output_tokens: %s", gotBody)
			}
			assertCodexResponsesBody(t, gotBody, true)
			for _, k := range tc.keep {
				if !bytes.Contains(gotBody, []byte(k)) {
					t.Fatalf("must keep %s: %s", k, gotBody)
				}
			}
		})
	}
}

func TestResponsesForwardsToolsAndFunctionCallOutput(t *testing.T) {
	var gotPath, gotAuth, gotAcct, gotAccept string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAcct = r.Header.Get("Chatgpt-Account-Id")
		gotAccept = r.Header.Get("Accept")
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "resp_tools",
			"object": "response",
			"output": []map[string]any{{
				"type":      "function_call",
				"call_id":   "call_lookup",
				"name":      "lookup",
				"arguments": `{"q":"x"}`,
			}},
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct_99", ExpiresAt: time.Now().Add(time.Hour)}
	raw := []byte(`{
		"model":"gpt-5",
		"tools":[{"type":"function","name":"lookup","description":"find things","parameters":{"type":"object"}}],
		"tool_choice":"auto",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"look this up"}]},
			{"type":"function_call","call_id":"call_prev","name":"lookup","arguments":"{\"q\":\"old\"}"},
			{"type":"function_call_output","call_id":"call_prev","output":"cached"},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"think"}]}
		],
		"stream_options":{"include_usage":true}
	}`)
	out, err := a.Responses(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "/responses") {
		t.Fatalf("path %s", gotPath)
	}
	if gotAuth != "Bearer tok" || gotAcct != "acct_99" {
		t.Fatalf("auth=%q acct=%q", gotAuth, gotAcct)
	}
	if gotAccept != "text/event-stream" {
		t.Fatalf("accept %s", gotAccept)
	}
	if bytes.Contains(gotBody, []byte("stream_options")) {
		t.Fatalf("must drop stream_options: %s", gotBody)
	}
	assertCodexResponsesBody(t, gotBody, true)
	for _, key := range []string{`"tools"`, `"tool_choice"`, `"function_call_output"`, `"function_call"`, `"reasoning"`, `"lookup"`} {
		if !bytes.Contains(gotBody, []byte(key)) {
			t.Fatalf("forwarded body missing %s: %s", key, gotBody)
		}
	}
	if !bytes.Contains(out, []byte(`"function_call"`)) {
		t.Fatalf("native response %s", out)
	}
}

func TestResponsesStreamForwardsTools(t *testing.T) {
	var gotBody []byte
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		gotAccept = r.Header.Get("Accept")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct_99", ExpiresAt: time.Now().Add(time.Hour)}
	raw := []byte(`{"model":"gpt-5","tools":[{"type":"function","name":"lookup"}],"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}],"stream_options":{"include_usage":true}}`)
	var out bytes.Buffer
	if err := a.ResponsesStream(context.Background(), raw, &out); err != nil {
		t.Fatal(err)
	}
	if gotAccept != "text/event-stream" {
		t.Fatalf("accept %s", gotAccept)
	}
	if bytes.Contains(gotBody, []byte("stream_options")) {
		t.Fatalf("must drop stream_options: %s", gotBody)
	}
	assertCodexResponsesBody(t, gotBody, true)
	if !bytes.Contains(gotBody, []byte(`"tools"`)) || !bytes.Contains(gotBody, []byte(`"function_call_output"`)) {
		t.Fatalf("stream body %s", gotBody)
	}
	if !strings.Contains(out.String(), "response.created") {
		t.Fatalf("sse %s", out.String())
	}
}

func TestChatMapsFunctionCallOutputToToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "resp_fc",
			"model": "gpt-5",
			"output": []map[string]any{
				{
					"type":      "function_call",
					"call_id":   "call_lookup",
					"name":      "lookup",
					"arguments": `{"q":"x"}`,
				},
			},
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct_99", ExpiresAt: time.Now().Add(time.Hour)}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "gpt-5",
		Raw:   []byte(`{"model":"gpt-5","tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"look"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(resp.Raw, []byte(`"tool_calls"`)) || !bytes.Contains(resp.Raw, []byte(`"lookup"`)) {
		t.Fatalf("chat response missing tool_calls: %s", resp.Raw)
	}
	if !bytes.Contains(resp.Raw, []byte(`"finish_reason":"tool_calls"`)) {
		t.Fatalf("finish_reason: %s", resp.Raw)
	}
}

func TestChatParsesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow"))
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct", ExpiresAt: time.Now().Add(time.Hour)}
	_, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "gpt-5",
		Raw:   []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`),
	})
	var he adapter.HTTPError
	if !errors.As(err, &he) || he.RetryAfter != 12*time.Second {
		t.Fatalf("retry-after = %v err = %v", he.RetryAfter, err)
	}
}

func TestChatPostsResponsesForcesStoreFalse(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          "resp_1",
			"output_text": "ok",
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct_99", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "gpt-5",
		Raw:   []byte(`{"model":"gpt-5","store":true,"max_output_tokens":64,"messages":[{"role":"user","content":"hi"}]}`),
	}); err != nil {
		t.Fatal(err)
	}
	assertCodexResponsesBody(t, gotBody, true)
	if bytes.Contains(gotBody, []byte(`"max_output_tokens"`)) {
		t.Fatalf("posted max_output_tokens: %s", gotBody)
	}

	gotBody = nil
	raw := []byte(`{"model":"gpt-5","store":true,"max_output_tokens":16,"input":"codex ping"}`)
	if _, err := a.Responses(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	assertCodexResponsesBody(t, gotBody, true)
	if bytes.Contains(gotBody, []byte(`"max_output_tokens"`)) {
		t.Fatalf("posted max_output_tokens: %s", gotBody)
	}
}

func assertCodexResponsesBody(t *testing.T, body []byte, stream bool) {
	t.Helper()
	var parsed struct {
		Store  *bool `json:"store"`
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if parsed.Store == nil || *parsed.Store {
		t.Fatalf("store must be false, got %s", body)
	}
	if bytes.Contains(body, []byte(`"store":true`)) {
		t.Fatalf("store true leaked: %s", body)
	}
	if parsed.Stream == nil || *parsed.Stream != stream {
		t.Fatalf("stream want %v, got %s", stream, body)
	}
}

func TestRefreshFormGrant(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(raw))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-at",
			"expires_in":   120,
		})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{RefreshToken: "old-rt", ExpiresAt: time.Now().Add(-time.Minute)}
	if err := a.ensureToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "old-rt" {
		t.Fatalf("%v", form)
	}
	if a.token.AccessToken != "new-at" || a.token.RefreshToken != "old-rt" {
		t.Fatalf("%#v", a.token)
	}
}

func TestNonStreamAssemblesCodexSSEAndStringInput(t *testing.T) {
	var gotBody []byte
	var gotAccept string
	sse := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"pon"}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_1","output_text":"pong","output":[{"type":"message","content":[{"type":"output_text","text":"pong"}]}]}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		gotAccept = r.Header.Get("Accept")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct_99", ExpiresAt: time.Now().Add(time.Hour)}

	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "gpt-5",
		Raw:   []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil || resp.Content != "pong" {
		t.Fatalf("%v %#v", err, resp)
	}
	assertCodexResponsesBody(t, gotBody, true)
	if gotAccept != "text/event-stream" {
		t.Fatalf("accept %s", gotAccept)
	}

	gotBody = nil
	out, err := a.Responses(context.Background(), []byte(`{"model":"gpt-5","input":"Reply with the single word pong."}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(gotBody, []byte(`"input":"Reply with the single word pong."`)) {
		t.Fatalf("string input forwarded: %s", gotBody)
	}
	if !bytes.Contains(gotBody, []byte(`"type":"input_text"`)) || !bytes.Contains(gotBody, []byte(`Reply with the single word pong.`)) {
		t.Fatalf("input list: %s", gotBody)
	}
	if bytes.Contains(out, []byte(`"type":"response.completed"`)) || !bytes.Contains(out, []byte(`"output_text":"pong"`)) {
		t.Fatalf("assembled response %s", out)
	}
}

func topLevelKeys(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return m
}

func assertServiceTier(t *testing.T, body []byte, want string) {
	t.Helper()
	got, ok := topLevelKeys(t, body)["service_tier"]
	if !ok {
		t.Fatalf("service_tier missing, want %q: %s", want, body)
	}
	if string(got) != `"`+want+`"` {
		t.Fatalf("service_tier=%s want %q: %s", got, want, body)
	}
}

func TestChatPostsServiceTierUpstream(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp_1", "output_text": "ok"})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct_99", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "gpt-5",
		Raw:   []byte(`{"model":"gpt-5","service_tier":"fast","reasoning_effort":"low","parallel_tool_calls":true,"messages":[{"role":"user","content":"hi"}]}`),
	}); err != nil {
		t.Fatal(err)
	}
	assertServiceTier(t, gotBody, "priority")
	keys := topLevelKeys(t, gotBody)
	if string(keys["parallel_tool_calls"]) != "true" {
		t.Fatalf("parallel_tool_calls: %s", gotBody)
	}
	if !bytes.Contains(keys["reasoning"], []byte(`"effort":"low"`)) {
		t.Fatalf("reasoning: %s", gotBody)
	}
	assertCodexResponsesBody(t, gotBody, true)
}

// routingHintServer records the x-codex-routing-hint of every request by path
// suffix; ok reports whether the header was present at all.
func routingHintServer(t *testing.T) (*Adapter, func(suffix string) (hint string, ok bool)) {
	t.Helper()
	var mu sync.Mutex
	hints := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := "/responses"
		if strings.HasSuffix(r.URL.Path, "/models") {
			key = "/models"
		}
		mu.Lock()
		hints[key] = r.Header.Values("x-codex-routing-hint")
		mu.Unlock()
		if key == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "gpt-5.5"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp_1", "output_text": "ok"})
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "tok", AccountID: "acct_99", ExpiresAt: time.Now().Add(time.Hour)}
	return a, func(suffix string) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		v, seen := hints[suffix]
		if !seen {
			t.Fatalf("no request to %s", suffix)
		}
		if len(v) == 0 {
			return "", false
		}
		return v[0], true
	}
}

func TestResponsesSendsRoutingHintModelOnly(t *testing.T) {
	a, hint := routingHintServer(t)
	if _, err := a.Responses(context.Background(), []byte(`{"model":"gpt-5.5","input":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if got, ok := hint("/responses"); !ok || got != "model=gpt-5.5" {
		t.Fatalf("hint=%q present=%v", got, ok)
	}
}

func TestResponsesSendsRoutingHintWithTier(t *testing.T) {
	a, hint := routingHintServer(t)
	if _, err := a.Responses(context.Background(), []byte(`{"model":"gpt-5.5","service_tier":"priority","input":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if got, ok := hint("/responses"); !ok || got != "model=gpt-5.5;tier=priority" {
		t.Fatalf("hint=%q present=%v", got, ok)
	}
}

func TestResponsesStreamSendsRoutingHint(t *testing.T) {
	a, hint := routingHintServer(t)
	if err := a.ResponsesStream(context.Background(), []byte(`{"model":"gpt-5.5","service_tier":"flex","input":"hi"}`), io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, ok := hint("/responses"); !ok || got != "model=gpt-5.5;tier=flex" {
		t.Fatalf("hint=%q present=%v", got, ok)
	}
}

func TestChatSendsRoutingHintWithTier(t *testing.T) {
	a, hint := routingHintServer(t)
	if _, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "gpt-5.5",
		Raw:   []byte(`{"model":"gpt-5.5","service_tier":"fast","messages":[{"role":"user","content":"hi"}]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if got, ok := hint("/responses"); !ok || got != "model=gpt-5.5;tier=priority" {
		t.Fatalf("hint=%q present=%v", got, ok)
	}
}

func TestChatRoutingHintOmitsDroppedTier(t *testing.T) {
	a, hint := routingHintServer(t)
	if _, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model: "gpt-5.5",
		Raw:   []byte(`{"model":"gpt-5.5","service_tier":"default","messages":[{"role":"user","content":"hi"}]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if got, ok := hint("/responses"); !ok || got != "model=gpt-5.5" {
		t.Fatalf("hint=%q present=%v", got, ok)
	}
}

func TestListModelsOmitsRoutingHint(t *testing.T) {
	a, hint := routingHintServer(t)
	if _, err := a.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, ok := hint("/models"); ok {
		t.Fatalf("/models got routing hint %q", got)
	}
}

func TestRoutingHintSkippedOnUnsafeValue(t *testing.T) {
	for name, body := range map[string]string{
		"semicolon model": `{"model":"gpt-5.5;tier=priority","input":"hi"}`,
		"control model":   `{"model":"gpt-5.5\u0001","input":"hi"}`,
		"space model":     `{"model":"gpt 5.5","input":"hi"}`,
		"semicolon tier":  `{"model":"gpt-5.5","service_tier":"priority;x=y","input":"hi"}`,
		"newline tier":    `{"model":"gpt-5.5","service_tier":"priority\n","input":"hi"}`,
		"empty model":     `{"input":"hi"}`,
	} {
		t.Run(name, func(t *testing.T) {
			a, hint := routingHintServer(t)
			if _, err := a.Responses(context.Background(), []byte(body)); err != nil {
				t.Fatal(err)
			}
			if got, ok := hint("/responses"); ok {
				t.Fatalf("unsafe value produced hint %q", got)
			}
		})
	}
}

func testAdapter(t *testing.T, base string) *Adapter {
	t.Helper()
	adp, err := New(adapter.Options{ID: "openai-oauth", BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	a := adp.(*Adapter)
	a.tokenURL = base + "/oauth/token"
	a.apiBase = strings.TrimRight(base, "/")
	a.httpClient = &http.Client{Timeout: 5 * time.Second}
	return a
}
