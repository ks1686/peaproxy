package translate

import (
	"strings"
	"testing"
)

func TestFidelityStrictSchemaNeverWeakened(t *testing.T) {
	in := []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","strict":true,"parameters":{"type":"object","additionalProperties":false}}}]}`)
	out, err := ToClaude(in, false)
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	if !strings.Contains(body, `"strict":true`) {
		t.Fatalf("strict flag dropped: %s", body)
	}
	if !strings.Contains(body, `"additionalProperties":false`) {
		t.Fatalf("schema weakened: %s", body)
	}
}

func TestUnsupportedBuiltinToolIsRejected(t *testing.T) {
	in := []byte(`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)
	if _, _, err := ToOpenAI(in); err == nil || !strings.Contains(err.Error(), "unsupported built-in tool") {
		t.Fatalf("error = %v", err)
	}
}

func TestNativePreservesOpaqueReasoning(t *testing.T) {
	in := []byte(`{"model":"m","max_tokens":8,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"hidden","signature":"sig_keep"},{"type":"text","text":"ok"}]}]}`)
	out, _, err := ToOpenAI(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "sig_keep") {
		t.Fatalf("opaque reasoning dropped: %s", out)
	}
}
