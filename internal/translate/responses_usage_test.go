package translate

import (
	"strings"
	"testing"
)

// A Responses client is told what a turn cost, or it cannot bill or budget.
//
// The field was absent entirely, which also meant PeaProxy's own ledger recorded
// no tokens for a streamed Responses call: unpriced, so the spend window became
// unmeasurable, so the ceiling refused everything. Both failures are one missing
// field.
func TestStreamedResponsesCarryUsage(t *testing.T) {
	upstream := "data: " + `{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}` + "\n\n" +
		"data: " + `{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":7}}` + "\n\n" +
		"data: [DONE]\n\n"

	var out strings.Builder
	if err := OpenAISSEToResponses(strings.NewReader(upstream), &out, "m"); err != nil {
		t.Fatal(err)
	}
	terminal := out.String()
	if !strings.Contains(terminal, `"input_tokens":40`) {
		t.Fatalf("the terminal event does not report input tokens:\n%s", terminal)
	}
	if !strings.Contains(terminal, `"output_tokens":7`) {
		t.Fatalf("the terminal event does not report output tokens:\n%s", terminal)
	}
}

// A provider that published nothing must not be reported as having used zero
// tokens. Unknown is not zero, and a fabricated zero reads to a client as a
// measurement.
func TestStreamedResponsesOmitUsageTheProviderNeverPublished(t *testing.T) {
	upstream := "data: " + `{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"

	var out strings.Builder
	if err := OpenAISSEToResponses(strings.NewReader(upstream), &out, "m"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"usage"`) {
		t.Fatalf("usage was invented for a provider that published none:\n%s", out.String())
	}
}
