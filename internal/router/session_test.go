package router

import (
	"net/http"
	"testing"
)

func TestSessionIDPrefersHeaderOverBody(t *testing.T) {
	h := make(http.Header)
	h.Set("X-Session-ID", "sess-9")
	body := []byte(`{"conversation_id":"other","messages":[{"role":"user","content":"hi"}]}`)
	if got := SessionID(h, body); got != "sess-9" {
		t.Fatalf("got %q", got)
	}
}

func TestSessionFromBodyUsesConversationField(t *testing.T) {
	body := []byte(`{"conversation_id":"conv-1","messages":[{"role":"user","content":"hi"}]}`)
	if got := SessionFromBody(body); got != "conv-1" {
		t.Fatalf("got %q", got)
	}
}

func TestSessionHashStaysStableWhenTheChatGrows(t *testing.T) {
	first := []byte(`{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello pea"}]}`)
	later := []byte(`{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello pea"},{"role":"assistant","content":"hi"},{"role":"user","content":"another turn"}]}`)
	a := SessionFromBody(first)
	b := SessionFromBody(later)
	if a == "" || a != b {
		t.Fatalf("a=%q b=%q", a, b)
	}
	other := SessionFromBody([]byte(`{"messages":[{"role":"user","content":"different"}]}`))
	if other == "" || other == a {
		t.Fatalf("other %q", other)
	}
}

func TestEmptyChatDoesNotShareASession(t *testing.T) {
	if got := SessionFromBody([]byte(`{"model":"m","messages":[]}`)); got != "" {
		t.Fatalf("got %q", got)
	}
}
