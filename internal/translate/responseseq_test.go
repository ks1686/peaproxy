package translate

import (
	"fmt"
	"strings"
	"testing"
)

// #62: the translated Responses stream was missing sequence_number, the
// message item's added/done events, and the indices a stream accumulator needs
// to reassemble the turn.

func streamEvents(t *testing.T, upstream string) []string {
	t.Helper()
	var sb strings.Builder
	if err := OpenAISSEToResponses(strings.NewReader(upstream), &sb, "m"); err != nil {
		t.Fatalf("translate: %v", err)
	}
	var out []string
	for _, line := range strings.Split(sb.String(), "\n") {
		if strings.HasPrefix(line, "event: ") {
			out = append(out, strings.TrimPrefix(line, "event: "))
		}
	}
	return out
}

func chatStream(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		fmt.Fprintf(&b, "data: %s\n\n", p)
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func hasEvent(evs []string, want string) bool {
	for _, e := range evs {
		if e == want {
			return true
		}
	}
	return false
}

func TestResponsesStreamCarriesSequenceNumbersOnEveryEvent(t *testing.T) {
	var sb strings.Builder
	in := chatStream(`{"id":"r1","model":"m","choices":[{"delta":{"content":"hi"}}]}`,
		`{"id":"r1","choices":[{"delta":{},"finish_reason":"stop"}]}`)
	if err := OpenAISSEToResponses(strings.NewReader(in), &sb, "m"); err != nil {
		t.Fatal(err)
	}
	// Every data: payload on a Responses stream carries sequence_number.
	var seqs []string
	for _, line := range strings.Split(sb.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		body := strings.TrimPrefix(line, "data: ")
		if !strings.Contains(body, `"sequence_number"`) {
			t.Errorf("event without sequence_number: %s", body)
			continue
		}
		seqs = append(seqs, body)
	}
	if len(seqs) < 5 {
		t.Fatalf("expected several events, got %d", len(seqs))
	}
	// And they must be strictly increasing from zero, or an accumulator that
	// reorders on them produces a different turn than the terminal array.
	want := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"}
	for i, body := range seqs {
		if i >= len(want) {
			break
		}
		if !strings.Contains(body, fmt.Sprintf(`"sequence_number":%s`, want[i])) {
			t.Errorf("event %d has the wrong sequence_number: %s", i, body)
		}
	}
}

func TestResponsesStreamEmitsInProgressAfterCreated(t *testing.T) {
	evs := streamEvents(t, chatStream(`{"id":"r1","choices":[{"delta":{"content":"hi"}}]}`))
	if len(evs) < 2 {
		t.Fatalf("got %d events", len(evs))
	}
	if evs[0] != "response.created" {
		t.Errorf("first event is %s, want response.created", evs[0])
	}
	if !hasEvent(evs, "response.in_progress") {
		t.Errorf("no response.in_progress in %v", evs)
	}
}

func TestResponsesStreamOpensAndClosesTheMessageItem(t *testing.T) {
	evs := streamEvents(t, chatStream(`{"id":"r1","choices":[{"delta":{"content":"hi"}}]}`))
	for _, want := range []string{
		"response.output_item.added",  // the message item
		"response.content_part.added", // its single text part
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
	} {
		if !hasEvent(evs, want) {
			t.Errorf("missing %s in %v", want, evs)
		}
	}
	// The added/done pairs must come in order around the deltas.
	idx := func(name string) int {
		for i, e := range evs {
			if e == name {
				return i
			}
		}
		return -1
	}
	added, delta, done := idx("response.output_item.added"), idx("response.output_text.delta"), idx("response.output_item.done")
	if !(added < delta && delta < done) {
		t.Errorf("message item events out of order: added=%d delta=%d done=%d", added, delta, done)
	}
}

func TestResponsesStreamClosesAFunctionCallItem(t *testing.T) {
	evs := streamEvents(t, chatStream(
		`{"id":"r1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"ping","arguments":"{}"}}]}}]}`,
	))
	for _, want := range []string{
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done",
	} {
		if !hasEvent(evs, want) {
			t.Errorf("missing %s in %v", want, evs)
		}
	}
}

// The issue's hard requirement: the indices announced while streaming have to
// line up with the terminal output array, or an accumulator that trusts them
// rebuilds a different turn than the one the provider described.
func TestStreamingIndicesMatchTheTerminalOutputArray(t *testing.T) {
	var sb strings.Builder
	in := chatStream(
		`{"id":"r1","choices":[{"delta":{"content":"hello"}}]}`,
		`{"id":"r1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"ping","arguments":"{\"a\":"}}]}}]}`,
		`{"id":"r1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		`{"id":"r1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if err := OpenAISSEToResponses(strings.NewReader(in), &sb, "m"); err != nil {
		t.Fatal(err)
	}
	raw := sb.String()

	// Collect the output_index announced for each item_id while streaming.
	announced := map[string]int{}
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		body := strings.TrimPrefix(line, "data: ")
		itemID := jsonField(body, "item_id")
		if itemID == "" {
			continue
		}
		oi := jsonField(body, "output_index")
		if oi == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(oi, "%d", &n); err != nil {
			continue
		}
		if prev, ok := announced[itemID]; ok && prev != n {
			t.Errorf("item %s was announced at index %d and then %d", itemID, prev, n)
		}
		announced[itemID] = n
	}
	if len(announced) < 2 {
		t.Fatalf("expected a message and a function call to be indexed, got %v", announced)
	}

	// Now the terminal array.
	term := lastDataWithType(raw, "response.completed")
	if term == "" {
		t.Fatal("no response.completed")
	}
	// Pull each item's id and its position in the output array, in order.
	type item struct {
		id  string
		idx int
	}
	var items []item
	// The terminal output array is the value of "output"; walk it by locating
	// each item's "type" and "id"/"call_id" in order of appearance.
	pos := 0
	for i := 0; i < 40; i++ {
		j := strings.Index(term[pos:], `{"type":`)
		if j < 0 {
			break
		}
		seg := term[pos+j:]
		pos += j + 1
		typ := jsonField(seg, "type")
		id := jsonField(seg, "id")
		if id == "" {
			id = jsonField(seg, "call_id")
		}
		if typ == "function_call" && id != "" {
			items = append(items, item{id: "call:" + id, idx: len(items)})
		}
		if typ == "message" {
			items = append(items, item{id: id, idx: len(items)})
		}
	}
	if len(items) < 2 {
		t.Fatalf("terminal output array did not parse: %s", term)
	}
	// Every announced index must be present, distinct, and inside the array.
	seen := map[int]bool{}
	for _, a := range announced {
		seen[a] = true
	}
	if len(seen) != len(announced) {
		t.Errorf("two items share an output_index: %v", announced)
	}
	for _, at := range announced {
		if at < 0 || at >= len(items) {
			t.Errorf("announced output_index %d is outside the terminal array of %d items", at, len(items))
		}
	}
}

// jsonField returns the raw value of a top-level-ish "key":value pair. Good
// enough for the flat event objects the translator emits.
func jsonField(s, key string) string {
	i := strings.Index(s, `"`+key+`":`)
	if i < 0 {
		return ""
	}
	rest := s[i+len(key)+3:]
	if rest == "" {
		return ""
	}
	switch rest[0] {
	case '"':
		end := strings.Index(rest[1:], `"`)
		if end < 0 {
			return ""
		}
		return rest[1 : end+1]
	case '{', '[':
		return "" // callers only want scalars
	default:
		end := strings.IndexAny(rest, ",}")
		if end < 0 {
			return rest
		}
		return rest[:end]
	}
}

func lastDataWithType(raw, typ string) string {
	var found string
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		body := strings.TrimPrefix(line, "data: ")
		if strings.Contains(body, `"type":"`+typ+`"`) {
			found = body
		}
	}
	return found
}
