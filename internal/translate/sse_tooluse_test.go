package translate

import (
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A tool call streams no bytes until the turn ends, so the client must hear a
// ping while its arguments buffer, and the tool call must survive the pings.
func TestOpenAISSEToClaudePingsWhileBufferingToolArgs(t *testing.T) {
	stream := toolCallStream(`{\"path\":\"a.txt\",`, `\"body\":\"long\"}`)

	got := translateSlowly(t, 1500*time.Millisecond, stream)

	if !strings.Contains(got.out.String(), "event: ping\ndata: {\"type\":\"ping\"}\n\n") {
		t.Fatalf("no ping while tool arguments buffered for 3s:\n%s", got.out.String())
	}
	if ping := got.firstAt(t, "ping"); !ping.Before(got.firstAt(t, "message_stop")) {
		t.Fatalf("first ping reached the client only with the finished turn:\n%s", got.out.String())
	}
	want := []replayedTool{{name: "write", input: `{"path":"a.txt","body":"long"}`}}
	if tools := replayClaudeBlocks(t, got.out.String()); !reflect.DeepEqual(tools, want) {
		t.Fatalf("tool inputs %+v, want %+v:\n%s", tools, want, got.out.String())
	}
}

// Pings are throttled: each follows at least a second in which the client
// heard nothing, however often upstream sends argument deltas.
func TestOpenAISSEToClaudePingsOnlyAfterASecondOfSilence(t *testing.T) {
	stream := toolCallStream(`{\"n\":[`, `1,`, `2,`, `3,`, `4,`, `5,`, `6,`, `7]}`)

	got := translateSlowly(t, 400*time.Millisecond, stream)

	pings := 0
	for i, ev := range got.events {
		if ev.name != "ping" {
			continue
		}
		pings++
		if i == 0 {
			t.Fatalf("ping before message_start:\n%s", got.out.String())
		}
		if silence := ev.at.Sub(got.events[i-1].at); silence < time.Second {
			t.Fatalf("ping after %v of silence, want at least 1s:\n%s", silence, got.out.String())
		}
	}
	if pings == 0 {
		t.Fatalf("no ping across 3.2s of buffered arguments:\n%s", got.out.String())
	}
}

func TestOpenAISSEToClaudeNoPing(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		gap    time.Duration
	}{
		{
			name:   "tool arguments while the clock stands still",
			chunks: toolCallStream(`{\"q\":`, `\"peas\"}`),
		},
		{
			name: "text turn however slow",
			chunks: []string{
				`data: {"id":"c1","model":"m","choices":[{"delta":{"content":"slow"}}]}` + "\n\n",
				`data: {"choices":[{"delta":{"content":" text"}}]}` + "\n\n",
				`data: {"choices":[{"finish_reason":"stop"}]}` + "\n\n",
				"data: [DONE]\n\n",
			},
			gap: 1500 * time.Millisecond,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := translateSlowly(t, tc.gap, tc.chunks)

			if strings.Contains(got.out.String(), "event: ping") {
				t.Fatalf("unexpected ping:\n%s", got.out.String())
			}
		})
	}
}

// The package writes wire JSON from structs so keys keep Anthropic's order; a
// Go map would sort them alphabetically.
func TestOpenAISSEToClaudeToolUseKeyOrder(t *testing.T) {
	in := strings.Join(toolCallStream(`{\"q\":`, `\"peas\"}`), "")
	var out strings.Builder

	if err := OpenAISSEToClaude(strings.NewReader(in), &out, "m"); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"write","input":{}}}` + "\n\n",
		"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"peas\"}"}}` + "\n\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
}

// toolCallStream is an OpenAI chat stream for one tool call, "write", whose
// arguments arrive as the given JSON-string-escaped parts, one chunk each.
func toolCallStream(parts ...string) []string {
	chunks := []string{`data: {"id":"c1","model":"m","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write","arguments":""}}]}}]}` + "\n\n"}
	for _, part := range parts {
		chunks = append(chunks, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"`+part+`"}}]}}]}`+"\n\n")
	}
	return append(chunks, `data: {"choices":[{"finish_reason":"tool_calls"}]}`+"\n\n", "data: [DONE]\n\n")
}

// translateSlowly runs OpenAISSEToClaude over chunks that arrive gap apart on
// a fake clock and returns what the client received, with arrival times.
func translateSlowly(t *testing.T, gap time.Duration, chunks []string) *eventRecorder {
	t.Helper()
	clock := useFakeSSEClock(t)
	sink := &eventRecorder{clock: clock}
	if err := OpenAISSEToClaude(&slowUpstream{clock: clock, chunks: chunks, gap: gap}, sink, "m"); err != nil {
		t.Fatal(err)
	}
	return sink
}

// fakeSSEClock stands in for sseNow; only slowUpstream moves it.
type fakeSSEClock struct{ now time.Time }

func useFakeSSEClock(t *testing.T) *fakeSSEClock {
	t.Helper()
	clock := &fakeSSEClock{now: time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)}
	prev := sseNow
	sseNow = func() time.Time { return clock.now }
	t.Cleanup(func() { sseNow = prev })
	return clock
}

// slowUpstream serves one chunk per Read and moves the clock forward by gap
// before every Read after the first, like a model that needs gap to produce
// each delta.
type slowUpstream struct {
	clock  *fakeSSEClock
	chunks []string
	gap    time.Duration
	reads  int
}

func (u *slowUpstream) Read(p []byte) (int, error) {
	if len(u.chunks) == 0 {
		return 0, io.EOF
	}
	if u.reads > 0 {
		u.clock.now = u.clock.now.Add(u.gap)
	}
	u.reads++
	n := copy(p, u.chunks[0])
	u.chunks[0] = u.chunks[0][n:]
	if u.chunks[0] == "" {
		u.chunks = u.chunks[1:]
	}
	return n, nil
}

// eventRecorder is the client: it keeps the SSE bytes and notes the fake-clock
// time at which each event line arrived.
type eventRecorder struct {
	clock   *fakeSSEClock
	events  []stampedEvent
	partial string
	out     strings.Builder
}

type stampedEvent struct {
	at   time.Time
	name string
}

func (r *eventRecorder) Write(p []byte) (int, error) {
	r.partial += string(p)
	for {
		line, rest, complete := strings.Cut(r.partial, "\n")
		if !complete {
			break
		}
		if name, isEvent := strings.CutPrefix(line, "event: "); isEvent {
			r.events = append(r.events, stampedEvent{at: r.clock.now, name: name})
		}
		r.partial = rest
	}
	return r.out.Write(p)
}

func (r *eventRecorder) firstAt(t *testing.T, name string) time.Time {
	t.Helper()
	for _, ev := range r.events {
		if ev.name == name {
			return ev.at
		}
	}
	t.Fatalf("no %s event:\n%s", name, r.out.String())
	return time.Time{}
}
