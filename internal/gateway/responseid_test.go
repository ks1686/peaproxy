package gateway

import (
	"bytes"
	"strings"
	"testing"
)

// The watcher sees exactly the bytes the client sees, so it has to survive a
// chunk boundary landing anywhere -- including mid-JSON, which is what a
// streaming upstream actually does.
func TestResponseIDWatcherAcrossChunkBoundaries(t *testing.T) {
	stream := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_abc","status":"completed"}}` + "\n\n" +
		"data: [DONE]\n\n"
	for _, size := range []int{1, 3, 7, 16, 31, 64, 200} {
		t.Run(strings.Repeat("x", 1)+"/"+itoa(size), func(t *testing.T) {
			var sink bytes.Buffer
			m := newResponseIDWatcher(&sink)
			for i := 0; i < len(stream); i += size {
				end := min(i+size, len(stream))
				if _, err := m.Write([]byte(stream[i:end])); err != nil {
					t.Fatal(err)
				}
			}
			if got := m.lastID(); got != "resp_abc" {
				t.Errorf("chunk size %d: id = %q, want resp_abc", size, got)
			}
			if sink.String() != stream {
				t.Errorf("chunk size %d: bytes were altered in transit", size)
			}
		})
	}
}

// response.created and response.completed both carry it; anything else must not
// be mistaken for one, including an unrelated event with a response field.
func TestResponseIDWatcherOnlyReadsCompletionEvents(t *testing.T) {
	var sink bytes.Buffer
	m := newResponseIDWatcher(&sink)
	m.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_created\"}}\n\n"))
	if got := m.lastID(); got != "resp_created" {
		t.Errorf("id = %q, want resp_created", got)
	}
	m.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_other\"}}\n\n"))
	if got := m.lastID(); got != "resp_created" {
		t.Errorf("a non-terminal event changed the id to %q", got)
	}
	m.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
	if got := m.lastID(); got != "resp_created" {
		t.Errorf("a delta changed the id to %q", got)
	}
	m.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_final\"}}\n\n"))
	if got := m.lastID(); got != "resp_final" {
		t.Errorf("id = %q, want resp_final", got)
	}
}

// A failed attempt's id must not survive into the next attempt.
func TestResponseIDWatcherReset(t *testing.T) {
	var sink bytes.Buffer
	m := newResponseIDWatcher(&sink)
	m.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_dead\"}}\n\n"))
	if m.lastID() == "" {
		t.Fatal("nothing was captured")
	}
	m.reset()
	if got := m.lastID(); got != "" {
		t.Errorf("after reset the id is still %q", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
