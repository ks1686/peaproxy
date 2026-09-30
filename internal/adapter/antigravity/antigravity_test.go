package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestGeminiStreamToolCalls(t *testing.T) {
	// Given two calls, including an empty-argument call, across upstream chunks.
	in := "data: " + `{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":{"path":"a"}}}]}}]}}` + "\n\ndata: " + `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"list","args":{}}}]},"finishReason":"STOP"}]}` + "\n\n"
	var out bytes.Buffer
	// When translating the stream.
	if err := geminiSSEToOpenAI(strings.NewReader(in), &out, "gemini-3-flash"); err != nil {
		t.Fatal(err)
	}
	// Then calls have distinct IDs, sequential indexes and terminal framing.
	type streamChunk struct {
		Choices []struct {
			Delta struct {
				Role      string `json:"role"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
	}
	var chunks []streamChunk
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "data: {") {
			chunks = append(chunks, streamChunk{})
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunks[len(chunks)-1]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(chunks) != 4 || chunks[0].Choices[0].Delta.Role != "assistant" || chunks[3].Choices[0].Finish != "tool_calls" {
		t.Fatalf("framing: %s", out.String())
	}
	first, second := chunks[1].Choices[0].Delta.ToolCalls[0], chunks[2].Choices[0].Delta.ToolCalls[0]
	if first.Index != 0 || second.Index != 1 || first.ID == "" || first.ID == second.ID || first.Type != "function" || first.Function.Name != "read" || first.Function.Arguments != `{"path":"a"}` || second.Function.Arguments != `{}` {
		t.Fatalf("calls: %s", out.String())
	}
	if !strings.HasSuffix(out.String(), "data: [DONE]\n\n") {
		t.Fatal(out.String())
	}
}

func TestGeminiToolHistory(t *testing.T) {
	// Given tool results in the opposite order to the calls.
	a := &Adapter{}
	raw := []byte(`{"messages":[{"role":"assistant","content":"checking","tool_calls":[{"id":"one","type":"function","function":{"name":"read","arguments":"{\"z\":1,\"a\":2}"}},{"id":"two","type":"function","function":{"name":"list","arguments":"{}"}}]},{"role":"tool","tool_call_id":"two","content":"listing"},{"role":"tool","tool_call_id":"one","content":"file"}]}`)
	// When building Cloud Code history.
	body, err := a.geminiBody(adapter.ChatRequest{Model: "gemini-3-flash", Raw: raw}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Then calls and grouped responses retain names and argument key order.
	var env struct {
		Request struct {
			Contents []struct {
				Role  string            `json:"role"`
				Parts []json.RawMessage `json:"parts"`
			} `json:"contents"`
		} `json:"request"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	c := env.Request.Contents
	if len(c) != 2 || c[0].Role != "model" || len(c[0].Parts) != 3 || c[1].Role != "user" || len(c[1].Parts) != 2 {
		t.Fatalf("history: %s", body)
	}
	for _, want := range []string{`"functionCall":{"name":"read","args":{"z":1,"a":2}}`, `"thoughtSignature":"skip_thought_signature_validator"`, `"functionResponse":{"name":"list","response":{"content":"listing"}}`, `"functionResponse":{"name":"read","response":{"content":"file"}}`} {
		if !bytes.Contains(body, []byte(want)) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
}

func TestGeminiParallelSameNameResultsPreserveAssociation(t *testing.T) {
	// Given two calls to the same function answered in reverse order.
	a := &Adapter{}
	raw := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.txt\"}"}},{"id":"b","type":"function","function":{"name":"read","arguments":"{\"path\":\"b.txt\"}"}}]},{"role":"tool","tool_call_id":"b","content":"CONTENTS_B"},{"role":"tool","tool_call_id":"a","content":"CONTENTS_A"}]}`)
	// When building Cloud Code history.
	body, err := a.geminiBody(adapter.ChatRequest{Model: "gemini-3-flash", Raw: raw}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Then the name-only responses follow the order of the calls they answer.
	var env geminiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	c := env.Request.Contents
	if len(c) != 2 || len(c[1].Parts) != 2 {
		t.Fatalf("history: %s", body)
	}
	var got []string
	for _, p := range c[1].Parts {
		if p.FunctionResponse == nil {
			t.Fatalf("history: %s", body)
		}
		got = append(got, string(p.FunctionResponse.Response.Content))
	}
	if got[0] != `"CONTENTS_A"` || got[1] != `"CONTENTS_B"` {
		t.Fatalf("responses %v, want call order [\"CONTENTS_A\" \"CONTENTS_B\"]: %s", got, body)
	}
}

func TestChatFunctionCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.WriteString(w, `{"response":{"candidates":[{"content":{"parts":[{"text":"private","thought":true},{"text":"checking"},{"functionCall":{"name":"read","args":{"filePath":"opencode.json"}},"thoughtSignature":"upstream-signature"}]}}]}}`)
		if err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv)
	a.token = oauth.Token{AccessToken: "test-token", ExpiresAt: time.Now().Add(time.Hour)}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "gemini-3-flash", Messages: []adapter.Message{{Role: "user", Content: "read"}}})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Choices []struct {
			Message chatMessage `json:"message"`
			Finish  string      `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp.Raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if resp.Content != "checking" || len(parsed.Choices) != 1 || parsed.Choices[0].Finish != "tool_calls" || len(parsed.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("response: %s", resp.Raw)
	}
	call := parsed.Choices[0].Message.ToolCalls[0]
	if call.ID == "" || call.Index != nil || call.Type != "function" || call.Function.Name != "read" || call.Function.Arguments != `{"filePath":"opencode.json"}` {
		t.Fatalf("call: %#v", call)
	}
}

func TestGeminiTextStreamFinish(t *testing.T) {
	var out bytes.Buffer
	in := "data: " + `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}` + "\n\ndata: [DONE]\n\n"
	if err := geminiSSEToOpenAI(strings.NewReader(in), &out, "gemini-3-flash"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"role":"assistant"`, `"content":"hello"`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
}

// observedReader records how much the translator had written before it first
// asked upstream for bytes.
type observedReader struct {
	io.Reader
	out           *bytes.Buffer
	beforeFirst   int
	firstReadSeen bool
}

func (o *observedReader) Read(p []byte) (int, error) {
	if !o.firstReadSeen {
		o.firstReadSeen = true
		o.beforeFirst = o.out.Len()
	}
	return o.Reader.Read(p)
}

func TestGeminiStreamWritesNothingBeforeUpstreamEvent(t *testing.T) {
	// Given an upstream that has sent headers but no event yet.
	var out bytes.Buffer
	in := &observedReader{Reader: strings.NewReader("data: " + `{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}` + "\n\n"), out: &out}
	// When translating the stream.
	if err := geminiSSEToOpenAI(in, &out, "gemini-3-flash"); err != nil {
		t.Fatal(err)
	}
	// Then no client bytes precede the first upstream event, so the gateway's
	// prelude timer can still fail a stalled account over.
	if in.beforeFirst != 0 {
		t.Fatalf("wrote %d bytes before reading upstream: %q", in.beforeFirst, out.String()[:in.beforeFirst])
	}
	for _, want := range []string{`"role":"assistant"`, `"content":"hi"`, `"finish_reason":"stop"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
}

func TestGeminiEmptyStreamStillCloses(t *testing.T) {
	var out bytes.Buffer
	if err := geminiSSEToOpenAI(strings.NewReader(""), &out, "gemini-3-flash"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"role":"assistant"`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
}

func TestGeminiStreamErrorPayloadFailsTheStream(t *testing.T) {
	text := "data: " + `{"response":{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}}` + "\n\n"
	quota := `{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`
	for _, tc := range []struct{ name, event string }{
		{"top-level error", quota},
		{"error under response", `{"response":` + quota + `}`},
	} {
		t.Run(tc.name+" after text", func(t *testing.T) {
			// Given a stream that fails after part of the answer.
			var out bytes.Buffer
			// When translating it.
			err := geminiSSEToOpenAI(strings.NewReader(text+"data: "+tc.event+"\n\n"), &out, "gemini-3-flash")
			// Then it fails instead of closing the partial answer as complete.
			if err == nil {
				t.Fatalf("error event swallowed: %s", out.String())
			}
			if !strings.Contains(out.String(), `"content":"partial"`) || strings.Contains(out.String(), `"finish_reason":"`) || strings.Contains(out.String(), "[DONE]") {
				t.Fatalf("partial answer closed as complete: %s", out.String())
			}
		})
		t.Run(tc.name+" as first event", func(t *testing.T) {
			// Given a stream whose first event is a quota error.
			var out bytes.Buffer
			// When translating it.
			err := geminiSSEToOpenAI(strings.NewReader("data: "+tc.event+"\n\n"), &out, "gemini-3-flash")
			// Then it fails as a model-scoped 429 with the reset hint and writes
			// nothing, so the gateway can cool the model and fail over.
			var he adapter.HTTPError
			if !errors.As(err, &he) || he.Status != http.StatusTooManyRequests || he.Scope != adapter.ScopeModel || he.RetryAfter != 30*time.Second {
				t.Fatalf("err = %#v, want a model-scoped 429 retrying in 30s", err)
			}
			if out.Len() != 0 {
				t.Fatalf("wrote %q before failing", out.String())
			}
		})
	}
	t.Run("status without a code", func(t *testing.T) {
		// Given a first event whose error carries only the google.rpc status name.
		var out bytes.Buffer
		// When translating the stream.
		err := geminiSSEToOpenAI(strings.NewReader("data: "+`{"error":{"message":"Internal error encountered.","status":"INTERNAL"}}`+"\n\n"), &out, "gemini-3-flash")
		// Then it fails as the HTTP status that name means on a non-streaming
		// reply, so the gateway classifies it the same way, and writes nothing.
		var he adapter.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusInternalServerError || out.Len() != 0 {
			t.Fatalf("err = %#v, output %q; want a 500 and no output", err, out.String())
		}
	})
}

func TestGeminiStreamMaxTokensDuringToolCall(t *testing.T) {
	call := `{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":{"path":"a"}}}]}`
	for _, tc := range []struct{ name, in string }{
		{"finish on the call chunk", "data: " + call + `,"finishReason":"MAX_TOKENS"}]}}` + "\n\n"},
		{"finish in a trailing chunk", "data: " + call + `}]}}` + "\n\ndata: " + `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"MAX_TOKENS"}]}}` + "\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a function call cut off by the output token limit.
			var out bytes.Buffer
			// When translating the stream.
			if err := geminiSSEToOpenAI(strings.NewReader(tc.in), &out, "gemini-3-flash"); err != nil {
				t.Fatal(err)
			}
			// Then the terminal chunk reports truncation, not a finished tool turn.
			finish := ""
			for _, line := range strings.Split(out.String(), "\n") {
				if !strings.HasPrefix(line, "data: {") {
					continue
				}
				var chunk struct {
					Choices []struct {
						Finish string `json:"finish_reason"`
					} `json:"choices"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
					t.Fatal(err)
				}
				if len(chunk.Choices) > 0 && chunk.Choices[0].Finish != "" {
					finish = chunk.Choices[0].Finish
				}
			}
			if finish != "length" {
				t.Fatalf("finish_reason = %q, want length: %s", finish, out.String())
			}
		})
	}
}

func TestGeminiChatMaxTokensDuringToolCall(t *testing.T) {
	// Given a non-stream function call cut off by the output token limit.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.WriteString(w, `{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":{"path":"a"}}}]},"finishReason":"MAX_TOKENS"}]}}`); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv)
	a.token = oauth.Token{AccessToken: "test-token", ExpiresAt: time.Now().Add(time.Hour)}
	// When completing the chat.
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "gemini-3-flash", Messages: []adapter.Message{{Role: "user", Content: "read"}}})
	if err != nil {
		t.Fatal(err)
	}
	// Then the choice reports truncation, not a finished tool turn.
	var parsed struct {
		Choices []struct {
			Finish string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp.Raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Choices) != 1 || parsed.Choices[0].Finish != "length" {
		t.Fatalf("finish_reason: %s", resp.Raw)
	}
}

func streamFinishReason(t *testing.T, in string) string {
	t.Helper()
	var out bytes.Buffer
	if err := geminiSSEToOpenAI(strings.NewReader(in), &out, "gemini-3-flash"); err != nil {
		t.Fatal(err)
	}
	finish := ""
	for _, line := range strings.Split(out.String(), "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var chunk struct {
			Choices []struct {
				Finish string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatal(err)
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Finish != "" {
			finish = chunk.Choices[0].Finish
		}
	}
	return finish
}

func chatFinishReason(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.WriteString(w, body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv)
	a.token = oauth.Token{AccessToken: "test-token", ExpiresAt: time.Now().Add(time.Hour)}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "gemini-3-flash", Messages: []adapter.Message{{Role: "user", Content: "read"}}})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Choices []struct {
			Finish string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp.Raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Choices) != 1 {
		t.Fatalf("choices: %s", resp.Raw)
	}
	return parsed.Choices[0].Finish
}

func TestGeminiSafetyStopIsContentFilter(t *testing.T) {
	partial := "data: " + `{"response":{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}}` + "\n\n"
	for _, reason := range []string{"SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY"} {
		blocked := `{"response":{"candidates":[{"finishReason":"` + reason + `"}]}}`
		blockedCall := `{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":{"path":"a"}}}]},"finishReason":"` + reason + `"}]}}`
		for _, tc := range []struct {
			name   string
			finish func(*testing.T) string
		}{
			{"stream without a tool call", func(t *testing.T) string { return streamFinishReason(t, partial+"data: "+blocked+"\n\n") }},
			{"stream with a tool call", func(t *testing.T) string { return streamFinishReason(t, "data: "+blockedCall+"\n\n") }},
			{"chat without a tool call", func(t *testing.T) string { return chatFinishReason(t, blocked) }},
			{"chat with a tool call", func(t *testing.T) string { return chatFinishReason(t, blockedCall) }},
		} {
			t.Run(reason+" "+tc.name, func(t *testing.T) {
				// Given a response Gemini stops for a safety reason.
				// When translating it to OpenAI.
				got := tc.finish(t)
				// Then it reports a content filter, ahead of any tool call.
				if got != "content_filter" {
					t.Fatalf("finish_reason = %q, want content_filter", got)
				}
			})
		}
	}
}

func TestGeminiPromptBlockIsContentFilter(t *testing.T) {
	for _, reason := range []string{"SAFETY", "OTHER", "BLOCKLIST", "PROHIBITED_CONTENT", "IMAGE_SAFETY", "BLOCK_REASON_UNSPECIFIED"} {
		feedback := `"promptFeedback":{"blockReason":"` + reason + `"}`
		for _, body := range []struct{ level, json string }{
			{"wrapped", `{"response":{` + feedback + `}}`},
			{"top-level", `{` + feedback + `}`},
		} {
			for _, tc := range []struct {
				name   string
				finish func(*testing.T) string
			}{
				{"stream", func(t *testing.T) string { return streamFinishReason(t, "data: "+body.json+"\n\n") }},
				{"chat", func(t *testing.T) string { return chatFinishReason(t, body.json) }},
			} {
				t.Run(reason+" "+body.level+" "+tc.name, func(t *testing.T) {
					// Given Gemini blocks the prompt and returns no candidates.
					// When translating the response to OpenAI.
					got := tc.finish(t)
					// Then it reports a content filter, not a clean stop.
					if got != "content_filter" {
						t.Fatalf("finish_reason = %q, want content_filter", got)
					}
				})
			}
		}
	}
}

func TestGeminiPromptFeedbackWithoutBlockKeepsCandidateFinish(t *testing.T) {
	body := `{"response":{"promptFeedback":{"safetyRatings":[{"category":"HARM_CATEGORY_HARASSMENT","probability":"NEGLIGIBLE"}]},"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}]}}`
	for _, tc := range []struct {
		name   string
		finish func(*testing.T) string
	}{
		{"stream", func(t *testing.T) string { return streamFinishReason(t, "data: "+body+"\n\n") }},
		{"chat", func(t *testing.T) string { return chatFinishReason(t, body) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given prompt feedback that carries only safety ratings and a normal candidate.
			// When translating the response to OpenAI.
			got := tc.finish(t)
			// Then the candidate's own finish reason is reported.
			if got != "stop" {
				t.Fatalf("finish_reason = %q, want stop", got)
			}
		})
	}
}

func TestGeminiOrphanToolResultBecomesUserText(t *testing.T) {
	a := &Adapter{}
	body, err := a.geminiBody(adapter.ChatRequest{Raw: []byte(`{"messages":[{"role":"tool","tool_call_id":"compacted","content":"result"}]}`)}, false)
	if err != nil {
		t.Fatalf("orphan tool result rejected: %v", err)
	}
	if bytes.Contains(body, []byte("functionResponse")) || !bytes.Contains(body, []byte(`"text":"result"`)) {
		t.Fatalf("orphan tool result not sent as text: %s", body)
	}
}

func TestGeminiRejectsInvalidToolHistory(t *testing.T) {
	for _, messages := range []string{
		`[{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"read","arguments":"[]"}}]}]`,
		`[{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"read","arguments":"{broken"}}]}]`,
	} {
		a := &Adapter{}
		if _, err := a.geminiBody(adapter.ChatRequest{Raw: []byte(`{"messages":` + messages + `}`)}, false); err == nil {
			t.Fatalf("accepted invalid history: %s", messages)
		}
	}
}

func TestGeminiToolOnlyAssistant(t *testing.T) {
	for _, content := range []string{`null`, `""`} {
		a := &Adapter{}
		body, err := a.geminiBody(adapter.ChatRequest{Raw: []byte(`{"messages":[{"role":"assistant","content":` + content + `,"tool_calls":[{"id":"a","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":[{"type":"text","text":"result"}]}]}`)}, false)
		if err != nil {
			t.Fatal(err)
		}
		var env geminiEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatal(err)
		}
		if len(env.Request.Contents[0].Parts) != 1 || env.Request.Contents[0].Parts[0].FunctionCall == nil || !bytes.Contains(body, []byte(`"content":[{"type":"text","text":"result"}]`)) {
			t.Fatalf("history: %s", body)
		}
	}
}

func TestGeminiEmptyToolCallArgumentsBecomeEmptyObject(t *testing.T) {
	for _, tc := range []struct{ name, arguments string }{
		{"empty", `""`},
		{"whitespace", `" \n\t"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a call to a zero-argument tool whose arguments are blank.
			a := &Adapter{}
			raw := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"now","arguments":` + tc.arguments + `}}]},{"role":"tool","tool_call_id":"a","content":"noon"}]}`)
			// When building Cloud Code history.
			body, err := a.geminiBody(adapter.ChatRequest{Model: "gemini-3-flash", Raw: raw}, false)
			if err != nil {
				t.Fatalf("blank arguments rejected: %v", err)
			}
			// Then the call sends empty-object args and keeps its result paired.
			var env geminiEnvelope
			if err := json.Unmarshal(body, &env); err != nil {
				t.Fatal(err)
			}
			c := env.Request.Contents
			if len(c) != 2 || len(c[0].Parts) != 1 || len(c[1].Parts) != 1 {
				t.Fatalf("history: %s", body)
			}
			call, result := c[0].Parts[0].FunctionCall, c[1].Parts[0].FunctionResponse
			if call == nil || string(call.Args) != "{}" || result == nil || result.Name != "now" {
				t.Fatalf("history: %s", body)
			}
		})
	}
}

func TestPublicInstalledAppClientAssembled(t *testing.T) {
	if !strings.HasPrefix(ClientID, "1071006060591-") {
		t.Fatalf("client id prefix %s", ClientID)
	}
	if !strings.HasSuffix(ClientID, "."+"apps.googleusercontent.com") {
		t.Fatalf("client id suffix %s", ClientID)
	}
	if !strings.HasPrefix(ClientSecret, "GOC"+"SPX-") {
		t.Fatal("secret prefix")
	}
	if len(ClientSecret) != 35 {
		t.Fatalf("secret len %d", len(ClientSecret))
	}
}

func TestAuthorizeURLUsesGoogleAntigravityClient(t *testing.T) {
	u, err := url.Parse(AuthorizeURL("st-g", RedirectURI))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "accounts.google.com" || !strings.Contains(u.Path, "/o/oauth2/") {
		t.Fatalf("%s %s", u.Host, u.Path)
	}
	q := u.Query()
	if q.Get("client_id") != ClientID {
		t.Fatalf("client_id %s", q.Get("client_id"))
	}
	if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" {
		t.Fatalf("%v", q)
	}
	if q.Get("redirect_uri") != RedirectURI {
		t.Fatalf("redirect %s", q.Get("redirect_uri"))
	}
	if !strings.Contains(q.Get("scope"), "cloud-platform") {
		t.Fatalf("scope %s", q.Get("scope"))
	}
}

func TestExchangeFormAndProjectDiscovery(t *testing.T) {
	var sawForm url.Values
	var loadBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/token":
			sawForm, _ = url.ParseQuery(string(raw))
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Errorf("content-type %s", r.Header.Get("Content-Type"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "ag-at", "refresh_token": "ag-rt", "expires_in": 3600,
			})
		case strings.Contains(r.URL.Path, "userinfo"):
			if r.Header.Get("Authorization") != "Bearer ag-at" {
				t.Errorf("userinfo auth %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"email": "g@x.y"})
		case strings.Contains(r.URL.Path, "loadCodeAssist"):
			loadBody = raw
			if r.Header.Get("Authorization") != "Bearer ag-at" {
				t.Errorf("load auth %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"cloudaicompanionProject": "proj-99"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	a := testAdapter(t, srv)
	a.pending = &pendingAuth{state: "st"}
	if err := a.AuthComplete(context.Background(), adapter.AuthSession{State: "st"}, "code-g"); err != nil {
		t.Fatal(err)
	}
	if sawForm.Get("grant_type") != "authorization_code" || sawForm.Get("code") != "code-g" {
		t.Fatalf("%v", sawForm)
	}
	if sawForm.Get("client_id") != ClientID || sawForm.Get("client_secret") == "" {
		t.Fatalf("client %v", sawForm)
	}
	if a.token.AccessToken != "ag-at" || a.token.Email != "g@x.y" {
		t.Fatalf("%#v", a.token)
	}
	if a.token.AccountID != "proj-99" || a.token.ExtraGet("project_id") != "proj-99" {
		t.Fatalf("project %#v", a.token)
	}
	if !bytes.Contains(loadBody, []byte(`"ideType"`)) {
		t.Fatalf("load body %s", loadBody)
	}
}

func TestListModelsAndChatUseCloudCode(t *testing.T) {
	var modelPath, chatPath, chatAuth, chatUA string
	var chatBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(r.URL.Path, "fetchAvailableModels"):
			modelPath = r.URL.Path
			if r.Header.Get("Authorization") != "Bearer live-at" {
				t.Errorf("models auth %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": map[string]any{
					"gemini-2.5-flash": map[string]string{"displayName": "Flash"},
				},
			})
		case strings.Contains(r.URL.Path, "generateContent"):
			chatPath = r.URL.Path
			chatAuth = r.Header.Get("Authorization")
			chatUA = r.Header.Get("User-Agent")
			chatBody = raw
			_ = json.NewEncoder(w).Encode(map[string]any{
				"response": map[string]any{
					"candidates": []map[string]any{{
						"content": map[string]any{
							"parts": []map[string]string{{"text": "hello gemini"}},
						},
					}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv)
	a.token = oauth.Token{
		AccessToken: "live-at",
		ExpiresAt:   time.Now().Add(time.Hour),
		AccountID:   "proj-1",
		Extra:       map[string]string{"project_id": "proj-1"},
	}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "gemini-2.5-flash" || !models[0].SubscriptionOAuth {
		t.Fatalf("%v %#v", err, models)
	}
	if !strings.Contains(modelPath, "fetchAvailableModels") {
		t.Fatalf("path %s", modelPath)
	}
	a.apiBase = "https://cloudcode-pa.googleapis.com"
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{
		Model:    "gemini-2.5-flash",
		Messages: []adapter.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil || resp.Content != "hello gemini" {
		t.Fatalf("%v %#v", err, resp)
	}
	if chatAuth != "Bearer live-at" {
		t.Fatalf("auth %q", chatAuth)
	}
	if !strings.Contains(chatPath, "generateContent") {
		t.Fatalf("chat path %s", chatPath)
	}
	if !bytes.Contains(chatBody, []byte(`"project"`)) || !bytes.Contains(chatBody, []byte("hi")) {
		t.Fatalf("chat body %s", chatBody)
	}
	if chatUA != UserAgent || strings.Contains(chatUA, "linux/amd64") || strings.Contains(chatUA, "2.9.1") {
		t.Fatalf("user agent %s", chatUA)
	}
	for _, needle := range []string{`"userAgent":"antigravity"`, `"requestType":"agent"`, `"requestId":"agent-`, `"sessionId":"-`} {
		if !bytes.Contains(chatBody, []byte(needle)) {
			t.Fatalf("missing %s in %s", needle, chatBody)
		}
	}
}

func TestChatKeepsImageAndTool(t *testing.T) {
	var chatBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "generateContent") {
			chatBody, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"response": map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]string{"text": "seen"}}}}}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv)
	a.token = oauth.Token{AccessToken: "live-at", ExpiresAt: time.Now().Add(time.Hour), Extra: map[string]string{"project_id": "proj-1"}}
	raw := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.com/pea.png"}}]}],"tools":[{"type":"function","function":{"name":"lookup","description":"find","parameters":{"type":"object"}}}]}`)
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "gemini-2.5-flash", Raw: raw})
	if err != nil || resp.Content != "seen" {
		t.Fatalf("%v %#v", err, resp)
	}
	for _, needle := range []string{`"fileUri":"https://example.com/pea.png"`, `"name":"lookup"`, `"text":"look"`} {
		if !bytes.Contains(chatBody, []byte(needle)) {
			t.Fatalf("missing %s in %s", needle, chatBody)
		}
	}
}

func TestChatStripsSchemaMetaFromTools(t *testing.T) {
	var chatBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "generateContent") {
			chatBody, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"response": map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]string{"text": "seen"}}}}}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv)
	a.token = oauth.Token{AccessToken: "live-at", ExpiresAt: time.Now().Add(time.Hour), Extra: map[string]string{"project_id": "proj-1"}}
	raw := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"path":{"type":"string","$id":"path"}}}}}]}`)
	if _, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "gemini-2.5-flash", Raw: raw}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(chatBody, []byte(`$schema`)) || bytes.Contains(chatBody, []byte(`$id`)) {
		t.Fatalf("schema meta leaked: %s", chatBody)
	}
	if !bytes.Contains(chatBody, []byte(`"name":"lookup"`)) || !bytes.Contains(chatBody, []byte(`"path"`)) {
		t.Fatalf("tool parameters dropped: %s", chatBody)
	}
}

func TestValidateRequiresToken(t *testing.T) {
	a := testAdapter(t, httptest.NewServer(http.NotFoundHandler()))
	if err := a.Validate(context.Background()); err == nil {
		t.Fatal("expected auth required")
	}
}

func testAdapter(t *testing.T, srv *httptest.Server) *Adapter {
	t.Helper()
	adp, err := New(adapter.Options{ID: "antigravity", SkipLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	a := adp.(*Adapter)
	a.tokenURL = srv.URL + "/token"
	a.userInfoURL = srv.URL + "/oauth2/v2/userinfo"
	a.apiBase = srv.URL
	a.dailyAPI = srv.URL
	a.httpClient = &http.Client{Timeout: 5 * time.Second}
	return a
}

func TestSanitizeGeminiSchemaCollapsesOptionalArray(t *testing.T) {
	in := `{"type":"object","properties":{"sorts":{"anyOf":[{"items":{"type":"string"},"type":"array"},{"type":"null"}],"title":"sorts","type":"array"},
		"pick":{"anyOf":[{"type":"string"},{"type":"integer"},{"type":"null"}]}}}`
	var got map[string]any
	if err := json.Unmarshal(sanitizeGeminiSchema(json.RawMessage(in)), &got); err != nil {
		t.Fatal(err)
	}
	want := `{"properties":{"pick":{"anyOf":[{"type":"string"},{"type":"integer"}],"nullable":true},"sorts":{"items":{"type":"string"},"nullable":true,"title":"sorts","type":"array"}},"type":"object"}`
	raw, _ := json.Marshal(got)
	if string(raw) != want {
		t.Fatalf("got  %s\nwant %s", raw, want)
	}
}

func TestProHighSlotsUseLiveUpstreamID(t *testing.T) {
	for in, want := range map[string]string{"gemini-3.1-pro-high": "gemini-pro-agent", "gemini-3-pro-high": "gemini-pro-agent", "gemini-3.1-pro-low": "gemini-3.1-pro-low"} {
		if got := upstreamModelID(in); got != want {
			t.Fatalf("%s -> %s, want %s", in, got, want)
		}
	}
}

func TestCloudCode429UsesBodyResetDelay(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Duration
	}{
		{"retry info", `{"error":{"code":429,"message":"slow down","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"3.5s"}]}}`, 3500 * time.Millisecond},
		{"quota reset metadata", `{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED","metadata":{"quotaResetDelay":"754.431528ms","model":"gemini-3-flash"}}]}}`, time.Second},
		{"resets in zero", `{"error":{"code":429,"message":"You have exhausted your capacity on this model. Resets in 0s.","status":"RESOURCE_EXHAUSTED"}}`, time.Second},
		{"weekly quota is honoured", `{"error":{"code":429,"message":"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 166h59m50s.","status":"RESOURCE_EXHAUSTED"}}`, 166*time.Hour + 59*time.Minute + 50*time.Second},
		{"hint beyond a week is capped", `{"error":{"code":429,"message":"Resets in 400h.","status":"RESOURCE_EXHAUSTED"}}`, 7 * 24 * time.Hour},
		{"no hint keeps default", `{"error":{"code":429,"message":"Resource has been exhausted"}}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pad := strings.Repeat(" ", 300)
			resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
			var he adapter.HTTPError
			if !errors.As(chatHTTPError(resp, []byte(pad+tc.body)), &he) {
				t.Fatal("not an HTTPError")
			}
			if he.RetryAfter != tc.want {
				t.Fatalf("RetryAfter = %s, want %s", he.RetryAfter, tc.want)
			}
			if len(he.Body) > 400 {
				t.Fatalf("error body not truncated: %d bytes", len(he.Body))
			}
		})
	}
}

func TestCloudCodeCapacityErrorIsModelScoped(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		resp := &http.Response{StatusCode: status, Header: http.Header{}}
		var he adapter.HTTPError
		if !errors.As(chatHTTPError(resp, []byte(`{"error":{"message":"No capacity available for model gemini-2.5-flash"}}`)), &he) {
			t.Fatalf("%d: not an HTTPError", status)
		}
		if he.Scope != adapter.ScopeModel {
			t.Fatalf("%d: scope %q, want model", status, he.Scope)
		}
	}
}

func TestSanitizeGeminiSchemaKeepsOnlyProtoFields(t *testing.T) {
	in := `{"$schema":"x","type":"object","additionalProperties":false,"properties":{
		"type":{"type":["string","null"],"const":"fixed"},
		"count":{"type":"integer","exclusiveMinimum":0,"maximum":9},
		"mode":{"oneOf":[{"type":"string","enum":["a"]},{"type":"number","exclusiveMaximum":5}]},
		"list":{"type":"array","items":{"type":"object","additionalProperties":{"type":"string"},"properties":{"title":{"type":"string","title":"T"}}}}
	},"required":["count"]}`
	var got map[string]any
	if err := json.Unmarshal(sanitizeGeminiSchema(json.RawMessage(in)), &got); err != nil {
		t.Fatal(err)
	}
	want := `{"properties":{"count":{"maximum":9,"minimum":0,"type":"integer"},"list":{"items":{"properties":{"title":{"title":"T","type":"string"}},"type":"object"},"type":"array"},"mode":{"anyOf":[{"enum":["a"],"type":"string"},{"maximum":5,"type":"number"}]},"type":{"enum":["fixed"],"nullable":true,"type":"string"}},"required":["count"],"type":"object"}`
	raw, _ := json.Marshal(got)
	if string(raw) != want {
		t.Fatalf("got  %s\nwant %s", raw, want)
	}
}

func sanitizedSchema(t *testing.T, in string) string {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(sanitizeGeminiSchema(json.RawMessage(in)), &got); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSanitizeGeminiSchemaPreservesTypeUnion(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"nullable scalar union", `{"type":["string","integer","null"]}`, `{"anyOf":[{"type":"string"},{"type":"integer"}],"nullable":true}`},
		{"array member keeps items", `{"type":["string","array"],"items":{"type":"string"}}`, `{"anyOf":[{"type":"string"},{"items":{"type":"string"},"type":"array"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a type list with more than one non-null type.
			// When reducing it to the Gemini Schema proto.
			got := sanitizedSchema(t, tc.in)
			// Then every listed type survives as its own anyOf branch.
			if got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestSanitizeGeminiSchemaTypeListIntersectsAnyOf(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{
			"branch constraints survive",
			`{"type":["string","integer"],"KEYWORD":[{"type":"string","maxLength":5},{"type":"integer"}]}`,
			`{"anyOf":[{"maxLength":5,"type":"string"},{"type":"integer"}]}`,
		},
		{
			"enum and minimum with null become nullable",
			`{"type":["string","integer","null"],"KEYWORD":[{"type":"string","enum":["a","b"]},{"type":"integer","minimum":1},{"type":"null"}]}`,
			`{"anyOf":[{"enum":["a","b"],"type":"string"},{"minimum":1,"type":"integer"}],"nullable":true}`,
		},
		{
			"array branch borrows the parent items",
			`{"type":["string","array"],"items":{"type":"string"},"KEYWORD":[{"type":"string","maxLength":3},{"type":"array","minItems":1}]}`,
			`{"anyOf":[{"maxLength":3,"type":"string"},{"items":{"type":"string"},"minItems":1,"type":"array"}]}`,
		},
		{
			"no overlap falls back to the union",
			`{"type":["string","integer"],"KEYWORD":[{"type":"boolean"}]}`,
			`{"anyOf":[{"type":"string"},{"type":"integer"},{"type":"boolean"}]}`,
		},
		{
			"a null branch is dropped when the list has no null",
			`{"type":["string","integer"],"KEYWORD":[{"type":"string","maxLength":2},{"type":"null"}]}`,
			`{"maxLength":2,"type":"string"}`,
		},
		{
			"untyped branches are kept",
			`{"type":["string","integer"],"KEYWORD":[{"maxLength":3},{"type":"integer"}]}`,
			`{"anyOf":[{"maxLength":3},{"type":"integer"}]}`,
		},
		{
			"an integer branch fits a number type",
			`{"type":["number","string"],"KEYWORD":[{"type":"integer","minimum":0},{"type":"boolean"}]}`,
			`{"minimum":0,"type":"integer"}`,
		},
	} {
		for _, keyword := range []string{"anyOf", "oneOf"} {
			t.Run(tc.name+" "+keyword, func(t *testing.T) {
				// Given a type list alongside anyOf or oneOf branches.
				// When reducing it to the Gemini Schema proto.
				got := sanitizedSchema(t, strings.ReplaceAll(tc.in, "KEYWORD", keyword))
				// Then only branches the type list allows remain, with their constraints.
				if got != tc.want {
					t.Fatalf("got  %s\nwant %s", got, tc.want)
				}
			})
		}
	}
}

func TestSanitizeGeminiSchemaSingleBranchKeepsConstraints(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{
			"conflicting properties and required",
			`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"anyOf":[{"type":"object","properties":{"b":{"type":"integer"}},"required":["b"]}]}`,
			`{"anyOf":[{"properties":{"b":{"type":"integer"}},"required":["b"],"type":"object"}],"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`,
		},
		{
			"conflicting type",
			`{"type":"number","anyOf":[{"type":"integer"}]}`,
			`{"anyOf":[{"type":"integer"}],"type":"number"}`,
		},
		{
			"conflicting items",
			`{"type":"array","items":{"type":"string"},"anyOf":[{"type":"array","items":{"maxLength":3}}]}`,
			`{"anyOf":[{"items":{"maxLength":3},"type":"array"}],"items":{"type":"string"},"type":"array"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a lone anyOf branch that disagrees with a key of its parent.
			// When reducing it to the Gemini Schema proto.
			got := sanitizedSchema(t, tc.in)
			// Then the branch stays whole rather than losing keys to the parent.
			if got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestSanitizeGeminiSchemaDoesNotEmitNonStringProtoEnums(t *testing.T) {
	// Given integer, boolean, mixed and string enums.
	in := `{"type":"object","properties":{"level":{"type":"integer","enum":[1,2]},"flag":{"type":"boolean","enum":[true]},"mixed":{"type":"string","enum":["a",1]},"mode":{"type":"string","enum":["a","b"]}}}`
	// When reducing them to the Gemini Schema proto, whose enum is a string list.
	got := sanitizedSchema(t, in)
	// Then only the all-string enum survives and the others keep just their type.
	want := `{"properties":{"flag":{"type":"boolean"},"level":{"type":"integer"},"mixed":{"type":"string"},"mode":{"enum":["a","b"],"type":"string"}},"type":"object"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestSanitizeGeminiSchemaTupleItems(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{
			"elements equal after sanitizing become one schema",
			`{"type":"array","items":[{"type":"string","$id":"a"},{"type":"string"}]}`,
			`{"items":{"type":"string"},"type":"array"}`,
		},
		{
			"distinct elements become anyOf in first-seen order",
			`{"type":"array","items":[{"type":"string"},{"type":"integer","exclusiveMinimum":0},{"type":"string","$id":"again"}]}`,
			`{"items":{"anyOf":[{"type":"string"},{"minimum":0,"type":"integer"}]},"type":"array"}`,
		},
		{
			"a null element folds into nullable",
			`{"type":"array","items":[{"type":"string"},{"type":"null"}]}`,
			`{"items":{"nullable":true,"type":"string"},"type":"array"}`,
		},
		{
			"an empty tuple drops items",
			`{"type":"array","description":"d","items":[]}`,
			`{"description":"d","type":"array"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a tuple-form items list, which the Gemini Schema proto lacks.
			// When reducing it to the Gemini Schema proto.
			got := sanitizedSchema(t, tc.in)
			// Then every element survives in the one items schema.
			if got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestSanitizeGeminiSchemaArrayParentGetsBranchItems(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{
			"array parent takes the branch items",
			`{"type":"array","description":"p","anyOf":[{"type":"array","description":"b","items":{"type":"string"}},{"type":"null"}]}`,
			`{"anyOf":[{"description":"b","items":{"type":"string"},"type":"array"}],"description":"p","items":{"type":"string"},"nullable":true,"type":"array"}`,
		},
		{
			"untyped parent stays without items",
			`{"description":"p","anyOf":[{"type":"array","description":"b","items":{"type":"string"}}]}`,
			`{"anyOf":[{"description":"b","items":{"type":"string"},"type":"array"}],"description":"p"}`,
		},
		{
			"array branches with different items give a union",
			`{"type":"array","anyOf":[{"type":"array","items":{"type":"string"}},{"type":"array","items":{"type":"integer"}}]}`,
			`{"anyOf":[{"items":{"type":"string"},"type":"array"},{"items":{"type":"integer"},"type":"array"}],"items":{"anyOf":[{"type":"string"},{"type":"integer"}]},"type":"array"}`,
		},
		{
			"array branches with equal items give that schema",
			`{"type":"array","anyOf":[{"type":"array","minItems":1,"items":{"type":"string"}},{"type":"array","maxItems":3,"items":{"type":"string"}}]}`,
			`{"anyOf":[{"items":{"type":"string"},"minItems":1,"type":"array"},{"items":{"type":"string"},"maxItems":3,"type":"array"}],"items":{"type":"string"},"type":"array"}`,
		},
		{
			"array branches and a null branch give nullable and a union",
			`{"type":"array","anyOf":[{"type":"array","items":{"type":"string"}},{"type":"array","items":{"type":"integer"}},{"type":"null"}]}`,
			`{"anyOf":[{"items":{"type":"string"},"type":"array"},{"items":{"type":"integer"},"type":"array"}],"items":{"anyOf":[{"type":"string"},{"type":"integer"}]},"nullable":true,"type":"array"}`,
		},
		{
			"one array branch among others lends its items",
			`{"type":"array","anyOf":[{"type":"array","items":{"type":"string"}},{"type":"string"}]}`,
			`{"anyOf":[{"items":{"type":"string"},"type":"array"},{"type":"string"}],"items":{"type":"string"},"type":"array"}`,
		},
		{
			"parent items are kept",
			`{"type":"array","items":{"type":"boolean"},"anyOf":[{"type":"array","items":{"type":"string"}},{"type":"array","items":{"type":"integer"}}]}`,
			`{"anyOf":[{"items":{"type":"string"},"type":"array"},{"items":{"type":"integer"},"type":"array"}],"items":{"type":"boolean"},"type":"array"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given anyOf branches with items under a parent that may lack them.
			// When reducing it to the Gemini Schema proto.
			got := sanitizedSchema(t, tc.in)
			// Then only an array parent gains the items Cloud Code requires of it.
			if got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestStreamErrorWithoutCodeStillClassifies(t *testing.T) {
	cases := []struct {
		name   string
		event  string
		status int
		retry  time.Duration
	}{
		{"status string", `{"error":{"message":"Individual quota reached. Resets in 2h.","status":"RESOURCE_EXHAUSTED"}}`, http.StatusTooManyRequests, 2 * time.Hour},
		{"nested response error", `{"response":{"error":{"status":"UNAVAILABLE","message":"try later"}}}`, http.StatusServiceUnavailable, 0},
		{"reset hint alone means quota", `{"error":{"message":"slow down","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"9s"}]}}`, http.StatusTooManyRequests, 9 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := streamEventError([]byte(tc.event))
			var he adapter.HTTPError
			if !errors.As(err, &he) {
				t.Fatalf("not an HTTPError: %v", err)
			}
			if he.Status != tc.status || he.RetryAfter != tc.retry {
				t.Fatalf("status %d retry %s, want %d %s", he.Status, he.RetryAfter, tc.status, tc.retry)
			}
			if he.Scope != adapter.ScopeModel {
				t.Fatalf("scope %q, want model", he.Scope)
			}
		})
	}
	if err := streamEventError([]byte(`{"error":{"message":"odd","status":"SOMETHING_NEW"}}`)); err == nil || errors.As(err, new(adapter.HTTPError)) {
		t.Fatalf("an unknown status without a code must stay a plain error: %v", err)
	}
}
