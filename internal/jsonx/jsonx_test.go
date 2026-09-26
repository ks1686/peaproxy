package jsonx

import (
	"bytes"
	"testing"
)

func TestSetStreamPreservesKeyOrder(t *testing.T) {
	in := []byte(`{"model":"claude-opus","system":"cache me","messages":[],"max_tokens":16}`)
	out := SetStream(in, true)
	if !bytes.Contains(out, []byte(`"model":"claude-opus","system":"cache me","messages":[]`)) {
		t.Fatalf("key order broken: %s", out)
	}
	if !bytes.Contains(out, []byte(`"stream":true`)) {
		t.Fatalf("missing stream: %s", out)
	}
}

func TestSetStreamReplacesExistingWithoutShuffle(t *testing.T) {
	in := []byte(`{"a":1,"stream":false,"z":2}`)
	out := SetStream(in, true)
	want := []byte(`{"a":1,"stream":true,"z":2}`)
	if !bytes.Equal(out, want) {
		t.Fatalf("got %s want %s", out, want)
	}
}

func TestPeekBody(t *testing.T) {
	p := PeekBody([]byte(`{"model":"llama3.2","stream":true}`))
	if p.Model != "llama3.2" || !p.Stream {
		t.Fatalf("%#v", p)
	}
}

func TestSetStreamNoopWhenAlreadySetPreservesBytes(t *testing.T) {
	in := []byte(`{"model":"claude-opus","system":"cache me","stream": true,"messages":[{"role":"user","content":"hi"}]}`)
	out := SetStream(in, true)
	if !bytes.Equal(out, in) {
		t.Fatalf("prompt-cache bytes mutated:\n got %s\nwant %s", out, in)
	}
}

func TestSetStreamDoesNotTouchNestedOrQuotedStream(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":"say \"stream\":true please"}],"stream":false,"tools":[{"function":{"name":"x","parameters":{"stream":false}}}]}`)
	out := SetStream(in, true)
	if bytes.Contains(out, []byte(`say \"stream\":false`)) {
		t.Fatalf("quoted stream rewritten: %s", out)
	}
	if !bytes.Contains(out, []byte(`"parameters":{"stream":false}`)) {
		t.Fatalf("nested stream rewritten: %s", out)
	}
	if !bytes.Contains(out, []byte(`"stream":true`)) {
		t.Fatalf("top-level stream not set: %s", out)
	}
	// Only the top-level false→true flip; prefix before stream must stay intact.
	if !bytes.HasPrefix(out, []byte(`{"model":"m","messages":[{"role":"user","content":"say \"stream\":true please"}],`)) {
		t.Fatalf("prefix shuffled: %s", out)
	}
}

func TestSetStreamIgnoresStreamOptionsKey(t *testing.T) {
	in := []byte(`{"model":"m","stream_options":{"include_usage":true},"messages":[]}`)
	out := SetStream(in, true)
	if !bytes.Contains(out, []byte(`"stream_options":{"include_usage":true}`)) {
		t.Fatalf("stream_options broken: %s", out)
	}
	if !bytes.Contains(out, []byte(`"stream":true`)) {
		t.Fatalf("missing top-level stream: %s", out)
	}
}

func TestDropTopLevelKeysNoopPreservesBytes(t *testing.T) {
	in := []byte(`{"model":"m","input":"ping"}`)
	out := DropTopLevelKeys(in, "stream_options")
	if !bytes.Equal(out, in) {
		t.Fatalf("got %s", out)
	}
}

func TestDropTopLevelKeysPreservesOrder(t *testing.T) {
	in := []byte(`{"model":"gpt-5","input":"ping","stream_options":{"include_usage":true},"max_output_tokens":16}`)
	out := DropTopLevelKeys(in, "stream_options")
	want := []byte(`{"model":"gpt-5","input":"ping","max_output_tokens":16}`)
	if !bytes.Equal(out, want) {
		t.Fatalf("got %s want %s", out, want)
	}
	if bytes.Contains(out, []byte("stream_options")) {
		t.Fatalf("key remained: %s", out)
	}
}
