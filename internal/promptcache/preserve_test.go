package promptcache

import (
	"bytes"
	"encoding/json"
	"testing"
)

// A prompt cache only hits when the bytes before the marker are unchanged.
// Rewriting the body to add one field therefore defeats the cache on every
// turn, and the cost of that is silent: requests still succeed.
//
// The body below is deliberately awkward -- keys out of alphabetical order,
// a space after a colon, an escaped quote, a float that a remarshal would
// normalise. Every one of those bytes must survive untouched.
func TestInsertBreakpointPreservesEveryOtherByte(t *testing.T) {
	body := []byte(`{
  "zebra": "last alphabetically but first in the body",
  "system": [
    {"type":"text","text":"base"},
    {"type": "text", "text": "the prefix"}
  ],
  "alpha": [1,2.50,1e3],
  "quote": "a \" b",
  "unicode": "caf\u00e9"
}`)
	out, err := insertBreakpoint(body)
	if err != nil {
		t.Fatal(err)
	}

	// The only permitted difference is the inserted field itself.
	withoutInserted := bytes.Replace(out,
		[]byte(`{"type": "text", "text": "the prefix","cache_control":{"type":"ephemeral"}}`),
		[]byte(`{"type": "text", "text": "the prefix"}`), 1)
	if !bytes.Equal(withoutInserted, body) {
		t.Fatalf("insertBreakpoint rewrote bytes outside the marker.\n got: %s\nwant: %s", withoutInserted, body)
	}
}

// Order is the point. A remarshal through a Go map sorts keys, which changes
// the prefix even when every value survives.
func TestInsertBreakpointDoesNotReorderKeys(t *testing.T) {
	body := []byte(`{"zebra":1,"system":[{"type":"text","text":"p"}],"alpha":2}`)
	out, err := insertBreakpoint(body)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Index(out, []byte(`"zebra"`)) > bytes.Index(out, []byte(`"alpha"`)) {
		t.Fatalf("key order changed: %s", out)
	}
}

// The marker still has to be real JSON that Anthropic will accept.
func TestInsertBreakpointProducesValidJSONWithTheMarker(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"p"}],"messages":[]}`)
	out, err := insertBreakpoint(body)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		System []struct {
			Type         string `json:"type"`
			Text         string `json:"text"`
			CacheControl *struct {
				Type string `json:"type"`
			} `json:"cache_control"`
		} `json:"system"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(doc.System) != 1 || doc.System[0].CacheControl == nil {
		t.Fatalf("marker missing: %s", out)
	}
	if doc.System[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("marker type = %q", doc.System[0].CacheControl.Type)
	}
	if doc.System[0].Text != "p" {
		t.Fatalf("text was altered: %q", doc.System[0].Text)
	}
}

// An empty block would produce a comma before the closing brace if the insert
// were done naively, which is not JSON.
func TestInsertBreakpointHandlesAnEmptyBlock(t *testing.T) {
	body := []byte(`{"system":[{},{"type":"text","text":"p"}]}`)
	out, err := insertBreakpoint(body)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string][]map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("empty block produced invalid JSON: %v\n%s", err, out)
	}
	if len(doc["system"]) != 2 {
		t.Fatalf("system changed shape: %s", out)
	}
}

// Everything the old implementation declined to touch must still be declined,
// and it must decline by returning the caller's exact bytes.
func TestInsertBreakpointLeavesUnmarkedBodiesByteIdentical(t *testing.T) {
	for name, body := range map[string]string{
		"no system":      `{"messages":[{"role":"user","content":"hi"}]}`,
		"already marked": `{"system":[{"type":"text","text":"p","cache_control":{"type":"ephemeral"}}]}`,
		"system is text": `{"system":"just a string"}`,
		"empty array":    `{"system":[]}`,
		"malformed":      `{"system":[`,
		"not an object":  `[1,2,3]`,
	} {
		raw := []byte(body)
		out, err := insertBreakpoint(raw)
		if err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
			continue
		}
		if !bytes.Equal(out, raw) {
			t.Errorf("%s: body was modified\n got: %s\nwant: %s", name, out, raw)
		}
	}
}

// The marker goes on the last block. Adding it to the first would cache a
// shorter prefix than the provider allows, wasting most of the saving.
func TestInsertBreakpointMarksTheLastBlockOnly(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"one"},{"type":"text","text":"two"}]}`)
	out, err := insertBreakpoint(body)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		System []map[string]json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.System) != 2 {
		t.Fatalf("block count changed: %s", out)
	}
	if _, ok := doc.System[0]["cache_control"]; ok {
		t.Fatalf("first block was marked: %s", out)
	}
	if _, ok := doc.System[1]["cache_control"]; !ok {
		t.Fatalf("last block was not marked: %s", out)
	}
}

// Applying twice must not add a second marker, and the second call must return
// exactly what the first produced.
func TestInsertBreakpointIsIdempotent(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"p"}],"model":"x"}`)
	once, err := insertBreakpoint(body)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := insertBreakpoint(once)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(once, twice) {
		t.Fatalf("second call changed the body:\n1: %s\n2: %s", once, twice)
	}
	if bytes.Count(twice, []byte("cache_control")) != 1 {
		t.Fatalf("marker added twice: %s", twice)
	}
}
