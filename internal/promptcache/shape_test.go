package promptcache

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The insertion works on the original bytes and looks for the last `}` in the
// final system element. When that element is not an object -- a bare string, or
// anything else -- that `}` can be a character inside the caller's text, and the
// marker lands in the middle of a prompt string, producing a body that is no
// longer the JSON the caller wrote.
func TestInsertionNeverTouchesANonObjectSystemElement(t *testing.T) {
	cases := map[string]string{
		"a bare string containing a brace":            `{"system":["read the docs } carefully"],"messages":[]}`,
		"a bare string containing a brace at the end": `{"system":["done }"],"messages":[]}`,
		"a number element":                            `{"system":[42],"messages":[]}`,
		"an empty array":                              `{"system":[],"messages":[]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := insertBreakpoint([]byte(body))
			if err != nil {
				t.Fatalf("insertBreakpoint: %v", err)
			}
			if !bytes.Equal(out, []byte(body)) {
				t.Fatalf("the body was modified:\n got: %s\nwant: %s", out, body)
			}
			if !json.Valid(out) {
				t.Fatalf("the result is not valid JSON: %s", out)
			}
		})
	}
}

// A brace inside an object's *value* is not a problem: the closing brace of the
// object is the last byte either way, and the marker goes in the right place.
// This guards against "fixing" the shape check by refusing every element whose
// text happens to contain a brace.
func TestBraceInsideAnObjectValueStillGetsItsMarker(t *testing.T) {
	for _, body := range []string{
		`{"system":[{"text":"a } b"}],"messages":[]}`,
		`{"system":[{"text":"}"}],"messages":[]}`,
	} {
		out, err := insertBreakpoint([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(out, []byte(body)) {
			t.Fatalf("no marker was inserted: %s", body)
		}
		if !json.Valid(out) {
			t.Fatalf("the result is not valid JSON: %s", out)
		}
		var doc struct {
			System []struct {
				Text         string          `json:"text"`
				CacheControl json.RawMessage `json:"cache_control"`
			} `json:"system"`
		}
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatal(err)
		}
		var want struct {
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
		}
		if err := json.Unmarshal([]byte(body), &want); err != nil {
			t.Fatal(err)
		}
		if doc.System[0].Text != want.System[0].Text {
			t.Fatalf("the caller's text was altered: %q became %q", want.System[0].Text, doc.System[0].Text)
		}
		if len(doc.System[0].CacheControl) == 0 {
			t.Fatalf("no cache_control was added: %s", out)
		}
	}
}

// The guard is about the element's shape, so the ordinary case must still work:
// an object element with no marker gets exactly one, and nothing else changes.
func TestInsertionStillWorksOnAnObjectElement(t *testing.T) {
	body := `{"system":[{"type":"text","text":"be brief"}],"messages":[]}`
	out, err := insertBreakpoint([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(out, []byte(body)) {
		t.Fatal("no marker was inserted into an object element")
	}
	if !json.Valid(out) {
		t.Fatalf("the result is not valid JSON: %s", out)
	}
	var doc struct {
		System []map[string]json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.System[0]["cache_control"]; !ok {
		t.Fatalf("no cache_control on the system block: %s", out)
	}
	if string(doc.System[0]["text"]) != `"be brief"` {
		t.Fatalf("the caller's text was altered: %s", out)
	}
}

// A string element with no brace at all was already safe, and must stay safe.
func TestStringSystemWithoutABraceIsUnchanged(t *testing.T) {
	body := `{"system":["plain"],"messages":[]}`
	out, err := insertBreakpoint([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, []byte(body)) {
		t.Fatalf("the body was modified: %s", out)
	}
}
