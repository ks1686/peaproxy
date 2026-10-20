package translate

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
)

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

func TestChatToResponsesKeepsImageURL(t *testing.T) {
	out, err := chatToResponses([]byte(`{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.com/pea.png"}}]}]}`), "gpt-5", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{`"type":"input_image"`, `https://example.com/pea.png`, `"text":"look"`} {
		if !bytes.Contains(out, []byte(needle)) {
			t.Fatalf("missing %s in %s", needle, out)
		}
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
	assertChatSSEFinish(t, got, "tool_calls")
}

// Codex sends a reasoning item at output_index 0, then a function call whose
// item id (fc_…) is not the call id (call_…). Argument deltas refer to the
// item id and the original output index. Those deltas must stay on the same
// chat tool_calls index as the call that carried the string id.
func TestChatStreamKeepsCodexToolDeltasOnCallIndex(t *testing.T) {
	in := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_9","encrypted_content":"enc"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_9","call_id":"call_9","name":"todowrite","arguments":""}}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_9","delta":"{\"todos\":"}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_9","delta":"[{\"content\":\"a\"}]}"}`,
		``,
		`data: {"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_8","call_id":"call_8","name":"other","arguments":""}}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_8","delta":"{}"}`,
		``,
		``,
	}, "\n")
	var out bytes.Buffer
	if err := responsesSSEToOpenAI(strings.NewReader(in), &out, "gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	var toolChunks int
	opened := map[int]string{}
	for _, payload := range sseDataPayloads(got) {
		if payload == "[DONE]" || !strings.Contains(payload, `"tool_calls"`) {
			continue
		}
		toolChunks++
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("chunk: %v\n%s", err, payload)
		}
		for _, tc := range chunk.Choices[0].Delta.ToolCalls {
			if tc.ID != "" {
				opened[tc.Index] = tc.ID
			} else if _, ok := opened[tc.Index]; !ok {
				t.Fatalf("argument delta opened a slot with no id\n%s", payload)
			}
			want := -1
			switch {
			case tc.ID == "call_9" || tc.Function.Name == "todowrite" || strings.Contains(tc.Function.Arguments, "todos") || strings.Contains(tc.Function.Arguments, "content"):
				want = 0
			case tc.ID == "call_8" || tc.Function.Name == "other" || tc.Function.Arguments == "{}":
				want = 1
			}
			if want < 0 || tc.Index != want {
				t.Fatalf("tool call index %d, want %d\n%s", tc.Index, want, payload)
			}
		}
	}
	if toolChunks < 4 {
		t.Fatalf("want added+delta for both calls, got %d tool chunks\n%s", toolChunks, got)
	}
	if !strings.Contains(got, `"todowrite"`) || !strings.Contains(got, `{\"todos\":`) || !strings.Contains(got, `"call_9"`) {
		t.Fatalf("missing todowrite deltas:\n%s", got)
	}
	if !strings.Contains(got, `"call_8"`) || !strings.Contains(got, `"other"`) {
		t.Fatalf("second call not kept:\n%s", got)
	}
	assertChatSSEFinish(t, got, "tool_calls")
}

func TestResponsesSSEToOpenAITextEmitsFinishReasonStop(t *testing.T) {
	in := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"hel"}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"lo"}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")
	var out bytes.Buffer
	if err := responsesSSEToOpenAI(strings.NewReader(in), &out, "gpt-5"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `"content":"hel"`) || !strings.Contains(got, `"content":"lo"`) {
		t.Fatalf("missing text deltas: %s", got)
	}
	if !strings.Contains(got, `"role":"assistant"`) {
		t.Fatalf("missing initial assistant role chunk: %s", got)
	}
	assertChatSSEFinish(t, got, "stop")
}

// The finish chunk is not assumed to be the second-to-last payload: a stream that
// reported usage puts a usage-only frame (empty choices) between the finish chunk
// and [DONE], so the chunk carrying finish_reason is found by scanning back.
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
			out := PrepareResponses([]byte(tc.in), tc.stream)
			assertCodexResponsesBody(t, out, tc.stream)
			if bytes.Contains(out, []byte(`"max_output_tokens":16`)) {
				t.Fatalf("max_output_tokens must be omitted: %s", out)
			}
			if strings.Contains(tc.in, `mention max_output_tokens please`) {
				if !bytes.Contains(out, []byte(`mention max_output_tokens please`)) ||
					bytes.Contains(out, []byte(`"input":"mention max_output_tokens please"`)) {
					t.Fatalf("string input must become a list that keeps the text: %s", out)
				}
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

func TestChatToResponsesFromMessagesForcesStoreFalse(t *testing.T) {
	out, err := chatToResponsesFromMessages("gpt-5", []adapter.Message{
		{Role: "user", Content: "hi"},
	}, chatCarry{}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertCodexResponsesBody(t, out, false)
}

func TestResponsesSSECarriesReasoningOpaqueBeforeFinish(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_9","encrypted_content":"enc","status":"completed","summary":[{"type":"summary_text","text":"skip me"}]}}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		``,
		`data: {"type":"response.completed","response":{"output":[{"type":"reasoning","id":"rs_9","encrypted_content":"enc","summary":[{"type":"summary_text","text":"skip me"}]}]}}`,
		``,
		``,
	}, "\n"))
	var buf bytes.Buffer
	if err := responsesSSEToOpenAI(in, &buf, "gpt-5"); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, `"content":"skip me"`) {
		t.Fatalf("reasoning streamed as content: %s", got)
	}
	opaqueAt := strings.Index(got, `"reasoning_opaque"`)
	finishAt := strings.Index(got, `"finish_reason"`)
	if opaqueAt < 0 || finishAt < 0 || opaqueAt > finishAt {
		t.Fatalf("opaque at %d finish at %d\n%s", opaqueAt, finishAt, got)
	}
	if strings.Count(got, `"reasoning_opaque"`) != 1 || !strings.Contains(got, `"rs_9"`) || !strings.Contains(got, `"content":"hello"`) {
		t.Fatalf("%s", got)
	}
}

func TestChatToResponsesRestoresReasoningBeforeText(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5",
		"messages":[{
			"role":"assistant",
			"content":"hello",
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}],
			"reasoning_opaque":[
				{"kind":"anthropic_thinking","thinking":"nope","signature":"sig"},
				{"kind":"responses_reasoning","id":"rs_9","encrypted_content":"enc","status":"completed","summary":[{"type":"summary_text","text":"skip me"}]}
			]
		}]
	}`)
	out, err := chatToResponses(raw, "gpt-5", false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("nope")) || bytes.Contains(out, []byte(`"signature"`)) {
		t.Fatalf("anthropic kind leaked: %s", out)
	}
	if bytes.Contains(out, []byte(`"output_text":"skip me"`)) {
		t.Fatalf("reasoning invented as assistant text: %s", out)
	}
	var parsed struct {
		Input []struct {
			Type             string          `json:"type"`
			ID               string          `json:"id"`
			EncryptedContent string          `json:"encrypted_content"`
			Role             string          `json:"role"`
			Summary          json.RawMessage `json:"summary"`
			Content          []struct {
				Text string `json:"text"`
			} `json:"content"`
			CallID string `json:"call_id"`
		} `json:"input"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if len(parsed.Input) < 3 {
		t.Fatalf("input %#v from %s", parsed.Input, out)
	}
	if parsed.Input[0].Type != "reasoning" || parsed.Input[0].ID != "rs_9" || parsed.Input[0].EncryptedContent != "enc" {
		t.Fatalf("first %#v", parsed.Input[0])
	}
	if !bytes.Contains(parsed.Input[0].Summary, []byte("skip me")) {
		t.Fatalf("summary %s", parsed.Input[0].Summary)
	}
	if parsed.Input[1].Role != "assistant" || parsed.Input[1].Content[0].Text != "hello" {
		t.Fatalf("text item %#v", parsed.Input[1])
	}
	if parsed.Input[2].Type != "function_call" || parsed.Input[2].CallID != "call_1" {
		t.Fatalf("call %#v", parsed.Input[2])
	}
}

func TestResponsesToChatCompletionCarriesReasoningOpaque(t *testing.T) {
	body := []byte(`{
		"id":"resp_1",
		"output":[
			{"type":"reasoning","id":"rs_9","encrypted_content":"enc","status":"completed","summary":[{"type":"summary_text","text":"skip me"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}
		]
	}`)
	out, content, err := responsesToChatCompletion("gpt-5", body)
	if err != nil {
		t.Fatal(err)
	}
	if content != "hello" {
		t.Fatalf("content %q", content)
	}
	if bytes.Contains(out, []byte(`"content":"skip me"`)) {
		t.Fatalf("reasoning invented as content: %s", out)
	}
	if !bytes.Contains(out, []byte(`"reasoning_opaque"`)) || !bytes.Contains(out, []byte(`"responses_reasoning"`)) || !bytes.Contains(out, []byte(`"rs_9"`)) {
		t.Fatalf("missing carry: %s", out)
	}
}

func TestResponsesStreamKeepsDeltaTextWhenCompletedObjectIsEmpty(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"pon"}`,
		`data: {"type":"response.output_text.delta","delta":"g"}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[{"type":"reasoning","id":"rs_9","summary":[{"type":"summary_text","text":"skip me"}]}]}}`,
		`data: [DONE]`,
	}, "\n")
	out, err := responsesStreamToJSON([]byte(sse))
	if err != nil {
		t.Fatal(err)
	}
	_, content, err := responsesToChatCompletion("gpt-5.5", out)
	if err != nil {
		t.Fatal(err)
	}
	if content != "pong" {
		t.Fatalf("content %q from %s", content, out)
	}
	if !bytes.Contains(out, []byte(`"rs_9"`)) {
		t.Fatalf("reasoning dropped: %s", out)
	}
}

func TestResponsesStreamFailedIsAnError(t *testing.T) {
	sse := "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_1\",\"status\":\"failed\"}}\n"
	if _, err := responsesStreamToJSON([]byte(sse)); err == nil {
		t.Fatal("failed stream returned success")
	}
}

func TestResponsesStreamIncompleteIsAnError(t *testing.T) {
	sse := `data: {"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete"}}` + "\\n"
	if _, err := ResponsesStreamToJSON([]byte(sse)); err == nil {
		t.Fatal("incomplete stream returned success")
	}
}

const chatToolsJSON = `"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]`

func TestChatToResponsesKeepsServiceTier(t *testing.T) {
	out, err := chatToResponses([]byte(`{"model":"gpt-5","service_tier":"priority","messages":[{"role":"user","content":"hi"}]}`), "gpt-5", true)
	if err != nil {
		t.Fatal(err)
	}
	assertServiceTier(t, out, "priority")
}

func TestChatToResponsesWithToolsKeepsServiceTier(t *testing.T) {
	out, err := chatToResponses([]byte(`{"model":"gpt-5","service_tier":"priority",`+chatToolsJSON+`,"messages":[{"role":"user","content":"hi"}]}`), "gpt-5", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := topLevelKeys(t, out)["tools"]; !ok {
		t.Fatalf("tools path not taken: %s", out)
	}
	assertServiceTier(t, out, "priority")
}

func TestChatToResponsesMapsFastToPriority(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-5","service_tier":"fast","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"gpt-5","service_tier":"fast",` + chatToolsJSON + `,"messages":[{"role":"user","content":"hi"}]}`,
	} {
		out, err := chatToResponses([]byte(body), "gpt-5", true)
		if err != nil {
			t.Fatal(err)
		}
		assertServiceTier(t, out, "priority")
	}
}

func TestChatToResponsesKeepsFlex(t *testing.T) {
	out, err := chatToResponses([]byte(`{"model":"gpt-5","service_tier":"flex","messages":[{"role":"user","content":"hi"}]}`), "gpt-5", true)
	if err != nil {
		t.Fatal(err)
	}
	assertServiceTier(t, out, "flex")
}

func TestChatToResponsesDropsUnknownServiceTier(t *testing.T) {
	for _, tier := range []string{"default", "auto", "scale", "bogus"} {
		t.Run(tier, func(t *testing.T) {
			for _, extra := range []string{"", chatToolsJSON + ","} {
				out, err := chatToResponses([]byte(`{"model":"gpt-5","service_tier":"`+tier+`",`+extra+`"messages":[{"role":"user","content":"hi"}]}`), "gpt-5", true)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := topLevelKeys(t, out)["service_tier"]; ok {
					t.Fatalf("service_tier %q forwarded: %s", tier, out)
				}
			}
		})
	}
}

func TestChatToResponsesOmitsAbsentFields(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"gpt-5",` + chatToolsJSON + `,"messages":[{"role":"user","content":"hi"}]}`,
	} {
		out, err := chatToResponses([]byte(body), "gpt-5", true)
		if err != nil {
			t.Fatal(err)
		}
		keys := topLevelKeys(t, out)
		for _, k := range []string{"service_tier", "reasoning", "parallel_tool_calls"} {
			if _, ok := keys[k]; ok {
				t.Fatalf("unexpected %s: %s", k, out)
			}
		}
	}
}

func TestChatToResponsesKeepsReasoningEffort(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-5","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"gpt-5","reasoning_effort":"high",` + chatToolsJSON + `,"messages":[{"role":"user","content":"hi"}]}`,
	} {
		out, err := chatToResponses([]byte(body), "gpt-5", true)
		if err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			Reasoning *struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.Reasoning == nil || parsed.Reasoning.Effort != "high" {
			t.Fatalf("reasoning.effort want high: %s", out)
		}
		if _, ok := topLevelKeys(t, out)["reasoning_effort"]; ok {
			t.Fatalf("chat reasoning_effort leaked: %s", out)
		}
	}
}

func TestChatToResponsesKeepsParallelToolCalls(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-5","parallel_tool_calls":false,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"gpt-5","parallel_tool_calls":false,` + chatToolsJSON + `,"messages":[{"role":"user","content":"hi"}]}`,
	} {
		out, err := chatToResponses([]byte(body), "gpt-5", true)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := topLevelKeys(t, out)["parallel_tool_calls"]
		if !ok || string(got) != "false" {
			t.Fatalf("parallel_tool_calls want false: %s", out)
		}
	}
}

// The Responses wire publishes usage on response.completed. The translator read
// that event to get the finish reason and dropped the counts, so every streamed
// Codex call reached the ledger with no token counts at all -- unmeasured, which
// is the state a spend ceiling refuses on.
//
// Measured 2026-10-11 on a live openai-oauth account: the native /v1/responses
// wire recorded 7/13 and the translated chat wire recorded nothing at all.
func TestResponsesSSEToOpenAIReportsUsage(t *testing.T) {
	in := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":7,"output_tokens":13,"total_tokens":20}}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")
	var out bytes.Buffer
	if err := responsesSSEToOpenAI(strings.NewReader(in), &out, "gpt-5"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	usageFrame := sseDataPayloads(got)
	last := usageFrame[len(usageFrame)-2] // before [DONE]
	for _, want := range []string{`"usage"`, `"prompt_tokens":7`, `"completion_tokens":13`} {
		if !strings.Contains(last, want) {
			t.Fatalf("the chunk before [DONE] missing %s: %s", want, last)
		}
	}
	assertChatSSEFinish(t, got, "stop")
}

// A response the upstream never completed has no counts to report. Zeros would
// read as a measured call that consumed nothing.
func TestResponsesSSEToOpenAIWithNoUsageReportsNone(t *testing.T) {
	in := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\ndata: [DONE]\n\n"
	var out bytes.Buffer
	if err := responsesSSEToOpenAI(strings.NewReader(in), &out, "gpt-5"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"usage"`) {
		t.Fatalf("a stream with no usage reported some: %s", out.String())
	}
}

// The non-stream path assembles the Responses stream locally -- Codex rejects
// stream:false -- and responsesStreamToJSON hands back the response.completed
// object verbatim, usage object and all. responsesToChatCompletion read the text
// and the tool calls out of it and left the usage behind, so the one path that
// could have reported tokens reported none.
//
// Measured 2026-10-11 on a live openai-oauth account: the native /v1/responses
// wire recorded 7/13 and the translated chat wire recorded nothing on either
// path. Fixed 2026-10-11.
func TestResponsesToChatCompletionCarriesUsage(t *testing.T) {
	body := []byte(`{"id":"resp_1","model":"gpt-5.6-luna","usage":{"input_tokens":7,"output_tokens":13,"total_tokens":20},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`)
	raw, content, err := responsesToChatCompletion("gpt-5.6-luna", body)
	if err != nil {
		t.Fatal(err)
	}
	if content != "hi" {
		t.Fatalf("content = %q, want %q", content, "hi")
	}
	for _, want := range []string{`"usage"`, `"prompt_tokens":7`, `"completion_tokens":13`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("chat.completion missing %s: %s", want, raw)
		}
	}
}

// A Responses object with no usage stays unmeasured. The key is omitted rather
// than sent as zeros, because zeros read as a measured call that cost nothing.
func TestResponsesToChatCompletionWithNoUsageOmitsIt(t *testing.T) {
	body := []byte(`{"id":"resp_1","model":"gpt-5.6-luna","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`)
	raw, _, err := responsesToChatCompletion("gpt-5.6-luna", body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"usage"`) {
		t.Fatalf("a response with no usage carried a usage object: %s", raw)
	}
}

// One half of the count is not a measurement. Reporting it would price the call
// at a fraction of what it cost, which is the quiet failure mode a ceiling
// cannot detect.
func TestResponsesToChatCompletionWithOneSidedUsageReportsNone(t *testing.T) {
	body := []byte(`{"id":"resp_1","model":"gpt-5.6-luna","usage":{"input_tokens":7,"output_tokens":0,"total_tokens":7},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`)
	raw, _, err := responsesToChatCompletion("gpt-5.6-luna", body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"usage"`) {
		t.Fatalf("a half-measured call was reported as measured: %s", raw)
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

// senpi marks explicit prompt-cache breakpoints on Responses input parts; the
// chatgpt.com Codex backend rejects the field with HTTP 400.
func TestPrepareResponsesDropsPromptCacheBreakpoints(t *testing.T) {
	in := `{"model":"gpt-6-astra","input":[` +
		`{"role":"developer","content":[{"type":"input_text","text":"rules","prompt_cache_breakpoint":{"mode":"explicit"}}]},` +
		`{"role":"user","content":[{"type":"input_text","text":"say prompt_cache_breakpoint verbatim"}]},` +
		`{"type":"function_call_output","call_id":"c1","output":"ok"}]}`
	out := PrepareResponses([]byte(in), true)
	assertCodexResponsesBody(t, out, true)
	if bytes.Contains(out, []byte(`"prompt_cache_breakpoint":`)) {
		t.Fatalf("prompt_cache_breakpoint must be dropped from input parts: %s", out)
	}
	for _, keep := range []string{`"text":"rules"`, `say prompt_cache_breakpoint verbatim`, `"call_id":"c1"`} {
		if !bytes.Contains(out, []byte(keep)) {
			t.Fatalf("lost %s: %s", keep, out)
		}
	}
}

func TestPrepareResponsesKeepsInputTextBytesWhenDroppingBreakpoints(t *testing.T) {
	in := `{"model":"gpt-6-astra","input":[{"role":"user","content":[{"type":"input_text","text":"a <b> & c","prompt_cache_breakpoint":{"mode":"explicit"}}]}]}`
	out := PrepareResponses([]byte(in), false)
	if !bytes.Contains(out, []byte(`"text":"a <b> & c"`)) {
		t.Fatalf("input text must pass through unescaped: %s", out)
	}
}
