package translate

import (
	"bytes"
	"strings"
	"testing"
)

func TestLooksLikeClaude(t *testing.T) {
	if LooksLikeClaude([]byte(`{"model":"gpt","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)) {
		t.Fatal("openai max_tokens must not look like claude")
	}
	if !LooksLikeClaude([]byte(`{"model":"claude","max_tokens":16,"system":"x","messages":[{"role":"user","content":"hi"}]}`)) {
		t.Fatal("system field is claude")
	}
	if LooksLikeClaude([]byte(`{"model":"gpt","max_tokens":16,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x"}}]}]}`)) {
		t.Fatal("image_url is openai")
	}
}

func TestToClaudeVisionDataURL(t *testing.T) {
	in := []byte(`{"model":"claude-sonnet","messages":[{"role":"user","content":[{"type":"text","text":"see"},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,abc"}}]}]}`)
	out, err := ToClaude(in, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"type":"image"`)) || !bytes.Contains(out, []byte(`"media_type":"image/jpeg"`)) {
		t.Fatalf("%s", out)
	}
	if bytes.Contains(out, []byte("data:image/jpeg")) {
		t.Fatalf("should strip data URL prefix: %s", out)
	}
	if !bytes.Contains(out, []byte(`"data":"abc"`)) {
		t.Fatalf("%s", out)
	}
}

func TestToClaudeDefaultsMaxTokensAndCanonicalModel(t *testing.T) {
	out, err := ToClaude([]byte(`{"model":"anthropic/claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"model":"claude-sonnet-4-5"`)) {
		t.Fatalf("canonical model: %s", out)
	}
	if !bytes.Contains(out, []byte(`"max_tokens":4096`)) {
		t.Fatalf("default max_tokens: %s", out)
	}
}

func TestCanonicalClaudeModel(t *testing.T) {
	cases := map[string]string{
		"claude-sonnet-5":           "claude-sonnet-5",
		"anthropic/claude-sonnet-5": "claude-sonnet-5",
		"claude-sonnet-4.5":         "claude-sonnet-4-5",
		"claude-opus-4.5":           "claude-opus-4-5",
		"  Claude-Haiku-4.5  ":      "claude-haiku-4-5",
		"anthropic/claude-opus-4-1": "claude-opus-4-1",
	}
	for in, want := range cases {
		if got := CanonicalClaudeModel(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestFromClaude(t *testing.T) {
	in := []byte(`{"id":"msg_1","model":"c","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}`)
	out, err := FromClaude(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"object":"chat.completion"`) || !strings.Contains(string(out), `"hi"`) {
		t.Fatalf("%s", out)
	}
}
