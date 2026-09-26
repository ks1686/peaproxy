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
