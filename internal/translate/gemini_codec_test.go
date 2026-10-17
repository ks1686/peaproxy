package translate

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
)

type testGeminiEnvelope struct {
	Request struct {
		Contents []struct {
			Parts []struct {
				FunctionCall *struct {
					Args json.RawMessage `json:"args"`
				} `json:"functionCall"`
				FunctionResponse *struct {
					Name     string `json:"name"`
					Response struct {
						Content json.RawMessage `json:"content"`
					} `json:"response"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	} `json:"request"`
}

func TestGeminiStreamToolCalls(t *testing.T) {
	// Given two calls, including an empty-argument call, across upstream chunks.
	in := "data: " + `{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":{"path":"a"}}}]}}]}}` + "\n\ndata: " + `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"list","args":{}}}]},"finishReason":"STOP"}]}` + "\n\n"
	var out bytes.Buffer
	// When translating the stream.
	if err := GeminiSSEToOpenAI(strings.NewReader(in), &out, "gemini-3-flash"); err != nil {
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
	raw := []byte(`{"messages":[{"role":"assistant","content":"checking","tool_calls":[{"id":"one","type":"function","function":{"name":"read","arguments":"{\"z\":1,\"a\":2}"}},{"id":"two","type":"function","function":{"name":"list","arguments":"{}"}}]},{"role":"tool","tool_call_id":"two","content":"listing"},{"role":"tool","tool_call_id":"one","content":"file"}]}`)
	// When building Cloud Code history.
	body, err := GeminiRequestJSON("", adapter.ChatRequest{Model: "gemini-3-flash", Raw: raw}, false)
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
	for _, want := range []string{`"functionCall":{"name":"read","args":{"z":1,"a":2},"id":"one"}`, `"thoughtSignature":"skip_thought_signature_validator"`, `"functionResponse":{"name":"list","response":{"content":"listing"},"id":"two"}`, `"functionResponse":{"name":"read","response":{"content":"file"},"id":"one"}`} {
		if !bytes.Contains(body, []byte(want)) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
}

func TestGeminiToolCallWithoutIDIsStillPopulated(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"","content":"ok"}]}`)
	body, err := GeminiRequestJSON("", adapter.ChatRequest{Model: "gpt-oss-120b-medium", Raw: raw}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"functionCall":{"name":"read","args":{},"id":"call_0"}`)) {
		t.Fatalf("missing minted id: %s", body)
	}
	if !bytes.Contains(body, []byte(`"id":"call_0"`)) {
		t.Fatalf("response id missing: %s", body)
	}
}

func TestGeminiParallelSameNameResultsPreserveAssociation(t *testing.T) {
	// Given two calls to the same function answered in reverse order.
	raw := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.txt\"}"}},{"id":"b","type":"function","function":{"name":"read","arguments":"{\"path\":\"b.txt\"}"}}]},{"role":"tool","tool_call_id":"b","content":"CONTENTS_B"},{"role":"tool","tool_call_id":"a","content":"CONTENTS_A"}]}`)
	// When building Cloud Code history.
	body, err := GeminiRequestJSON("", adapter.ChatRequest{Model: "gemini-3-flash", Raw: raw}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Then the name-only responses follow the order of the calls they answer.
	var env testGeminiEnvelope
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

func TestGeminiTextStreamFinish(t *testing.T) {
	var out bytes.Buffer
	in := "data: " + `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}` + "\n\ndata: [DONE]\n\n"
	if err := GeminiSSEToOpenAI(strings.NewReader(in), &out, "gemini-3-flash"); err != nil {
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
	if err := GeminiSSEToOpenAI(in, &out, "gemini-3-flash"); err != nil {
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
	if err := GeminiSSEToOpenAI(strings.NewReader(""), &out, "gemini-3-flash"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"role":"assistant"`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
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
			if err := GeminiSSEToOpenAI(strings.NewReader(tc.in), &out, "gemini-3-flash"); err != nil {
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

func TestGeminiOrphanToolResultBecomesUserText(t *testing.T) {
	body, err := GeminiRequestJSON("", adapter.ChatRequest{Raw: []byte(`{"messages":[{"role":"tool","tool_call_id":"compacted","content":"result"}]}`)}, false)
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
		if _, err := GeminiRequestJSON("", adapter.ChatRequest{Raw: []byte(`{"messages":` + messages + `}`)}, false); err == nil {
			t.Fatalf("accepted invalid history: %s", messages)
		}
	}
}

func TestGeminiToolOnlyAssistant(t *testing.T) {
	for _, content := range []string{`null`, `""`} {
		body, err := GeminiRequestJSON("", adapter.ChatRequest{Raw: []byte(`{"messages":[{"role":"assistant","content":` + content + `,"tool_calls":[{"id":"a","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":[{"type":"text","text":"result"}]}]}`)}, false)
		if err != nil {
			t.Fatal(err)
		}
		var env testGeminiEnvelope
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
			raw := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"now","arguments":` + tc.arguments + `}}]},{"role":"tool","tool_call_id":"a","content":"noon"}]}`)
			// When building Cloud Code history.
			body, err := GeminiRequestJSON("", adapter.ChatRequest{Model: "gemini-3-flash", Raw: raw}, false)
			if err != nil {
				t.Fatalf("blank arguments rejected: %v", err)
			}
			// Then the call sends empty-object args and keeps its result paired.
			var env testGeminiEnvelope
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

func TestSanitizeGeminiSchemaCollapsesOptionalArray(t *testing.T) {
	in := `{"type":"object","properties":{"sorts":{"anyOf":[{"items":{"type":"string"},"type":"array"},{"type":"null"}],"title":"sorts","type":"array"},
		"pick":{"anyOf":[{"type":"string"},{"type":"integer"},{"type":"null"}]}}}`
	var got map[string]any
	if err := json.Unmarshal(SanitizeGeminiSchema(json.RawMessage(in)), &got); err != nil {
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
		if got := GeminiUpstreamModelID(in); got != want {
			t.Fatalf("%s -> %s, want %s", in, got, want)
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
	if err := json.Unmarshal(SanitizeGeminiSchema(json.RawMessage(in)), &got); err != nil {
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
	if err := json.Unmarshal(SanitizeGeminiSchema(json.RawMessage(in)), &got); err != nil {
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

// Gemini publishes usageMetadata on its final streamed chunk. geminiSSEToOpenAI
// read that chunk for the finish reason and dropped the counts, so every
// streamed Antigravity call reached the ledger with no token counts: unmeasured,
// which is the state a spend ceiling refuses on.
//
// Measured 2026-10-11 on a live google-oauth account: no usage recorded on
// either the streamed or the non-streamed translated chat wire.
func TestGeminiStreamReportsUsage(t *testing.T) {
	in := "data: " + `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":7,"totalTokenCount":19}}` + "\n\n"
	var out bytes.Buffer
	if err := GeminiSSEToOpenAI(strings.NewReader(in), &out, "gemini-2.5-flash-lite"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	payloads := geminiSSEPayloads(got)
	if len(payloads) < 2 {
		t.Fatalf("want a usage frame then [DONE], got %d payloads: %q", len(payloads), got)
	}
	if last := payloads[len(payloads)-1]; last != "[DONE]" {
		t.Fatalf("last payload = %q, want [DONE]", last)
	}
	usageFrame := payloads[len(payloads)-2]
	for _, want := range []string{`"usage"`, `"prompt_tokens":12`, `"completion_tokens":7`} {
		if !strings.Contains(usageFrame, want) {
			t.Fatalf("the chunk before [DONE] missing %s: %s", want, usageFrame)
		}
	}
}

func geminiSSEPayloads(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	return out
}
