package antigravity

import (
	"errors"
	"strings"
	"testing"
)

// The same guarantee the openai_oauth wire makes: a stream cut after bytes
// reached the client is announced rather than left to look like a quiet
// finish. This is the google-oauth account's path.
func TestCutMidStreamIsNotSilent(t *testing.T) {
	r := &errAfterReader{
		data: strings.Join([]string{
			`data: {"candidates":[{"content":{"parts":[{"text":"partial"}]},"index":0}],"modelVersion":"m"}`,
			``,
			``,
		}, "\n"),
		err: errors.New("unexpected EOF"),
	}

	var out strings.Builder
	_ = geminiSSEToOpenAI(r, &out, "gemini-3.1-pro")
	got := out.String()

	if !strings.Contains(got, "partial") {
		t.Fatalf("the text that did arrive was discarded: %q", got)
	}
	if strings.Contains(got, `"finish_reason"`) {
		t.Fatal("a finish_reason was fabricated for a stream that was cut short")
	}
	if !strings.Contains(got, `"error"`) {
		t.Fatalf("the cut was silent; the client cannot distinguish it from a quiet finish:\n%s", got)
	}
}

// A clean end still finishes normally. The error path must not leak into it.
func TestCleanEndStillFinishesNormally(t *testing.T) {
	in := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"complete"}]},"index":0}],"modelVersion":"m"}`,
		``,
		`data: {"candidates":[{"finishReason":"STOP","index":0}],"modelVersion":"m"}`,
		``,
	}, "\n")

	var out strings.Builder
	if err := geminiSSEToOpenAI(strings.NewReader(in), &out, "gemini-3.1-pro"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `"finish_reason"`) {
		t.Fatalf("a clean completion did not report a finish reason: %s", got)
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
