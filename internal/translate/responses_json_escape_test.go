package translate

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The .done events were formatted with Go's %q, whose escapes (\x1b, \v,
// \U000e0067) are not JSON. Every event a Responses client receives must parse.
func TestOpenAISSEToResponsesEmitsValidJSONForControlAndAstralText(t *testing.T) {
	content, _ := json.Marshal("red \x1b[31mtext\x1b[0m tab\v flag \U0001F3F4\U000E0067\U000E0062\U000E007F")
	args, _ := json.Marshal("{\"q\":\"a\vb\"}")
	in := "data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{\"content\":" + string(content) + "}}]}\n\n" +
		"data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"ping\",\"arguments\":" + string(args) + "}}]}}]}\n\n" +
		"data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	var out bytes.Buffer
	if err := OpenAISSEToResponses(strings.NewReader(in), &out, "m"); err != nil {
		t.Fatal(err)
	}
	for _, p := range sseDataPayloads(out.String()) {
		if p == "[DONE]" {
			continue
		}
		if !json.Valid([]byte(p)) {
			t.Fatalf("invalid JSON event: %s", p)
		}
	}
	if !strings.Contains(out.String(), "response.output_text.done") {
		t.Fatalf("no output_text.done event to check:\n%s", out.String())
	}
}
