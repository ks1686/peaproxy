package openai_oauth

import (
	"encoding/json"
	"strings"
	"testing"
)

// #60: service_tier, reasoning_effort and parallel_tool_calls were carried into
// the Responses body from a chat request; verbosity and prompt_cache_key were
// silently dropped.

func chatWith(t *testing.T, extra string) []byte {
	t.Helper()
	return []byte(`{"model":"gpt-5-codex","messages":[{"role":"user","content":"hi"}]` + extra + `}`)
}

func responsesOf(t *testing.T, chat []byte) map[string]any {
	t.Helper()
	out, err := chatToResponses(chat, "gpt-5-codex", false)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Codex gates verbosity on the catalog, so a model that supports it gets
// text.verbosity and one that does not is left alone rather than 400ing.
func TestVerbosityIsCarriedForModelsThatSupportIt(t *testing.T) {
	got := responsesOf(t, chatWith(t, `,"verbosity":"low"`))
	text, ok := got["text"].(map[string]any)
	if !ok {
		t.Fatalf("no text block in %v", got)
	}
	if text["verbosity"] != "low" {
		t.Errorf("verbosity = %v, want low", text["verbosity"])
	}
}

func TestVerbosityIsDroppedForModelsThatDoNotSupportIt(t *testing.T) {
	for _, model := range []string{"o3", "o4-mini", "gpt-4.1"} {
		chat := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"verbosity":"high"}`)
		out, err := chatToResponses(chat, model, false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), `"verbosity"`) {
			t.Errorf("%s does not support verbosity but it was sent: %s", model, out)
		}
	}
}

// Only the three levels Codex accepts; an invented one would be a 400. Case and
// surrounding space are normalised rather than rejected, since "LOW" is plainly
// the same request as "low" and rejecting it helps nobody.
func TestVerbosityAcceptsOnlyLowMediumHigh(t *testing.T) {
	for _, bad := range []string{"verbose", "none", "1", "lowest", "med", ""} {
		got := responsesOf(t, chatWith(t, `,"verbosity":"`+bad+`"`))
		if text, ok := got["text"].(map[string]any); ok && text["verbosity"] != nil {
			t.Errorf("verbosity %q was sent through as %v", bad, text["verbosity"])
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"low", "low"}, {"medium", "medium"}, {"high", "high"},
		{"LOW", "low"}, {"High", "high"}, {" low ", "low"},
	} {
		got := responsesOf(t, chatWith(t, `,"verbosity":"`+tc.in+`"`))
		text, ok := got["text"].(map[string]any)
		if !ok || text["verbosity"] != tc.want {
			t.Errorf("verbosity %q became %v, want %q", tc.in, got["text"], tc.want)
		}
	}
}

// Codex always sends a prompt cache key, and it is what keeps a long
// conversation's prefix cached across turns.
func TestPromptCacheKeyIsPassedThrough(t *testing.T) {
	got := responsesOf(t, chatWith(t, `,"prompt_cache_key":"session-abc-123"`))
	if got["prompt_cache_key"] != "session-abc-123" {
		t.Errorf("prompt_cache_key = %v, want session-abc-123", got)
	}
}

// An unbounded key is a header-sized field upstream; a control byte in one is a
// header injection attempt. Both are dropped rather than forwarded.
func TestPromptCacheKeyIsDroppedWhenUnusable(t *testing.T) {
	long := strings.Repeat("k", 257)
	cases := map[string]string{
		"too long":     long,
		"exactly 256":  strings.Repeat("k", 256),
		"control byte": "bad\x00key",
		"newline":      "bad\nkey",
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(key)
			got := responsesOf(t, chatWith(t, `,"prompt_cache_key":`+string(raw)))
			if name == "exactly 256" {
				if got["prompt_cache_key"] != key {
					t.Errorf("a 256-byte key was dropped: %v", got)
				}
				return
			}
			if _, present := got["prompt_cache_key"]; present {
				t.Errorf("an unusable key was forwarded: %v", got)
			}
		})
	}
}

// max_output_tokens is rejected by the ChatGPT backend and Codex never sends
// it, so it is dropped rather than forwarded into a 400.
func TestMaxTokensIsDroppedWithAReason(t *testing.T) {
	got := responsesOf(t, chatWith(t, `,"max_tokens":64`))
	for _, k := range []string{"max_output_tokens", "max_tokens"} {
		if _, present := got[k]; present {
			t.Errorf("%s was forwarded to the ChatGPT backend: %v", k, got)
		}
	}
}

// An absent verbosity or prompt_cache_key must not create an empty text block.
func TestNoVerbosityMeansNoTextBlock(t *testing.T) {
	got := responsesOf(t, chatWith(t, ""))
	if _, present := got["text"]; present {
		t.Errorf("an empty text block was added: %v", got)
	}
	if _, present := got["prompt_cache_key"]; present {
		t.Errorf("an absent prompt_cache_key was added: %v", got)
	}
}
