package openai_oauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
	assertCodexResponsesBody(t, body, false)
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
			keep: []string{`"input":"codex ping"`},
		},
		{
			name: "quoted stream_options in input kept",
			raw:  `{"model":"gpt-5","input":"mention stream_options please","stream_options":{"include_usage":true},"max_output_tokens":16}`,
			keep: []string{`"input":"mention stream_options please"`},
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
			assertCodexResponsesBody(t, gotBody, false)
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
	if gotAccept != "application/json" {
		t.Fatalf("accept %s", gotAccept)
	}
	if bytes.Contains(gotBody, []byte("stream_options")) {
		t.Fatalf("must drop stream_options: %s", gotBody)
	}
	assertCodexResponsesBody(t, gotBody, false)
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

func TestChatToResponsesMapsToolsAndToolMessages(t *testing.T) {
	in := []byte(`{
		"model":"gpt-5",
		"tools":[{"type":"function","function":{"name":"lookup","description":"find things","parameters":{"type":"object"}}}],
		"tool_choice":{"type":"function","function":{"name":"lookup"}},
		"messages":[
			{"role":"user","content":"look this up"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"found it"}
		]
	}`)
	out, err := chatToResponses(in, "gpt-5", false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(`"messages"`)) {
		t.Fatalf("leaked chat messages: %s", out)
	}
	if !bytes.Contains(out, []byte(`"tools"`)) || !bytes.Contains(out, []byte(`"tool_choice"`)) {
		t.Fatalf("missing tools/tool_choice: %s", out)
	}
	if !bytes.Contains(out, []byte(`"type":"function_call"`)) || !bytes.Contains(out, []byte(`"call_id":"call_1"`)) {
		t.Fatalf("missing function_call item: %s", out)
	}
	if !bytes.Contains(out, []byte(`"type":"function_call_output"`)) || !bytes.Contains(out, []byte(`"output":"found it"`)) {
		t.Fatalf("missing function_call_output: %s", out)
	}
	var parsed struct {
		Tools []struct {
			Type     string `json:"type"`
			Name     string `json:"name"`
			Function *struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tools) != 1 || parsed.Tools[0].Name != "lookup" || parsed.Tools[0].Function != nil {
		t.Fatalf("tools must be Responses-shaped, got %s", out)
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

func TestChatToResponsesPreservesModelAndUserText(t *testing.T) {
	out, err := chatToResponses([]byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`), "gpt-5", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"model":"gpt-5"`)) || !bytes.Contains(out, []byte("hello")) {
		t.Fatalf("%s", out)
	}
	if bytes.Contains(out, []byte(`"messages"`)) {
		t.Fatalf("leaked chat messages: %s", out)
	}
}

func TestChatStreamMapsFunctionCallEvents(t *testing.T) {
	in := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","item_id":"call_1","delta":"{\"q\":\"x\"}"}`,
		``,
		``,
	}, "\n")
	var out bytes.Buffer
	if err := responsesSSEToOpenAI(strings.NewReader(in), &out, "gpt-5"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `"tool_calls"`) || !strings.Contains(got, `"lookup"`) {
		t.Fatalf("chat stream missing tool_calls: %s", got)
	}
	if !strings.Contains(got, `[DONE]`) {
		t.Fatalf("%s", got)
	}
}

func TestPrepareResponsesForcesStoreFalseAndDropsMaxOutputTokens(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		stream bool
	}{
		{name: "omitted store", in: `{"model":"gpt-5","input":"hi"}`},
		{name: "store true", in: `{"model":"gpt-5","input":"hi","store":true}`},
		{name: "store false", in: `{"model":"gpt-5","input":"hi","store":false}`},
		{name: "max_output_tokens stripped", in: `{"model":"gpt-5","input":"hi","max_output_tokens":16,"store":true}`, stream: true},
		{name: "quoted max_output_tokens in input kept", in: `{"model":"gpt-5","input":"mention max_output_tokens please","max_output_tokens":16}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := prepareResponses([]byte(tc.in), tc.stream)
			assertCodexResponsesBody(t, out, tc.stream)
			if bytes.Contains(out, []byte(`"max_output_tokens":16`)) {
				t.Fatalf("max_output_tokens must be omitted: %s", out)
			}
			if strings.Contains(tc.in, `mention max_output_tokens please`) &&
				!bytes.Contains(out, []byte(`"input":"mention max_output_tokens please"`)) {
				t.Fatalf("quoted max_output_tokens in input rewritten: %s", out)
			}
		})
	}
}

func TestChatToResponsesForcesStoreFalse(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		stream bool
	}{
		{name: "omitted store on chat", in: `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`},
		{name: "store true on chat", in: `{"model":"gpt-5","store":true,"max_output_tokens":32,"messages":[{"role":"user","content":"hi"}]}`},
		{name: "store true on native input", in: `{"model":"gpt-5","store":true,"max_output_tokens":16,"input":"hi"}`},
		{name: "omitted store on native input", in: `{"model":"gpt-5","input":"hi"}`},
		{
			name:   "tools path store true",
			in:     `{"model":"gpt-5","store":true,"max_output_tokens":8,"tools":[{"type":"function","function":{"name":"lookup"}}],"messages":[{"role":"user","content":"hi"}]}`,
			stream: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := chatToResponses([]byte(tc.in), "gpt-5", tc.stream)
			if err != nil {
				t.Fatal(err)
			}
			assertCodexResponsesBody(t, out, tc.stream)
			if bytes.Contains(out, []byte(`"max_output_tokens"`)) {
				t.Fatalf("max_output_tokens must be omitted: %s", out)
			}
		})
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
	assertCodexResponsesBody(t, gotBody, false)
	if bytes.Contains(gotBody, []byte(`"max_output_tokens"`)) {
		t.Fatalf("posted max_output_tokens: %s", gotBody)
	}

	gotBody = nil
	raw := []byte(`{"model":"gpt-5","store":true,"max_output_tokens":16,"input":"codex ping"}`)
	if _, err := a.Responses(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	assertCodexResponsesBody(t, gotBody, false)
	if bytes.Contains(gotBody, []byte(`"max_output_tokens"`)) {
		t.Fatalf("posted max_output_tokens: %s", gotBody)
	}
}

func TestChatToResponsesFromMessagesForcesStoreFalse(t *testing.T) {
	out, err := chatToResponsesFromMessages("gpt-5", []adapter.Message{
		{Role: "user", Content: "hi"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertCodexResponsesBody(t, out, false)
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
