package openai_oauth

import (
	"errors"
	"strings"
	"testing"
)

// A stream that dies mid-body has already put bytes in the client's hands. If
// nothing further is written, the client sees a stream that simply stops, which
// is indistinguishable from a provider that finished quietly -- and a client
// checking for a terminal event reports "stream ended without finish_reason"
// with nothing to act on.
func TestCutMidStreamIsNotSilent(t *testing.T) {
	r := &errAfterReader{
		data: strings.Join([]string{
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","content":[]}}`,
			``,
			`data: {"type":"response.output_text.delta","output_index":0,"delta":"partial answer"}`,
			``,
			``,
		}, "\n"),
		err: errors.New("unexpected EOF"),
	}

	var out strings.Builder
	_ = responsesSSEToOpenAI(r, &out, "gpt-6-astra")
	got := out.String()

	if !strings.Contains(got, "partial answer") {
		t.Fatalf("the text that did arrive was discarded: %q", got)
	}
	if strings.Contains(got, `"finish_reason"`) {
		t.Fatal("a finish_reason was fabricated for a stream that was cut short")
	}
	if !strings.Contains(got, `"error"`) {
		t.Fatalf("the cut was silent; the client is told nothing and cannot distinguish it from a quiet finish:\n%s", got)
	}
}

// A clean end is the opposite case and must stay a normal completion.
func TestCleanEndStillFinishesNormally(t *testing.T) {
	in := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","content":[]}}`,
		``,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"complete answer"}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5}}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	var out strings.Builder
	if err := responsesSSEToOpenAI(strings.NewReader(in), &out, "gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `"finish_reason":"stop"`) {
		t.Fatalf("a clean completion did not report stop: %s", got)
	}
	if strings.Contains(got, `"error"`) {
		t.Fatalf("a clean completion was reported as an error: %s", got)
	}
}

type errAfterReader struct {
	data string
	err  error
	done bool
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	return copy(p, r.data), nil
}
