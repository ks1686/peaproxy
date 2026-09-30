package clients

import (
	"encoding/json"
	"testing"
)

func TestUpsertJSONKeyDoesNotAliasSourceCapacity(t *testing.T) {
	body := []byte(`{"a":{"b":1}}`)
	for _, key := range []string{"c", "d", "e"} {
		var err error
		if body, err = upsertJSONKey(body, key, key); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(body) {
			t.Fatalf("upsert %s produced invalid json: %s", key, body)
		}
	}
	if want := `{"a":{"b":1},"c":"c","d":"d","e":"e"}`; string(body) != want {
		t.Fatalf("got %s want %s", body, want)
	}
}

func TestDetectIndent(t *testing.T) {
	for _, tc := range []struct {
		body      string
		indent    string
		multiline bool
	}{
		{"{\n  \"a\": 1\n}", "  ", true},
		{"{\n    \"a\": {\n        \"b\": 1\n    }\n}", "    ", true},
		{"{\n\t\"a\": 1\n}", "\t", true},
		{`{"a": 1}`, "", false},
	} {
		indent, multiline := detectIndent([]byte(tc.body))
		if indent != tc.indent || multiline != tc.multiline {
			t.Errorf("detectIndent(%q) = %q, %v; want %q, %v", tc.body, indent, multiline, tc.indent, tc.multiline)
		}
	}
}

func TestUpsertJSONKeyReplacesExistingKeyInPlace(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"a":1,"env":{"x":1},"z":2}`, `{"a":1,"env":{"y":2},"z":2}`},
		{`{"env":{"x":1},"z":2}`, `{"env":{"y":2},"z":2}`},
		{`{"a":1,"env":{"x":1}}`, `{"a":1,"env":{"y":2}}`},
		{`{"a":1}`, `{"a":1,"env":{"y":2}}`},
		{`{}`, `{"env":{"y":2}}`},
	} {
		got, err := upsertJSONKey([]byte(tc.body), "env", json.RawMessage(`{"y":2}`))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("upsert env into %s = %s; want %s", tc.body, got, tc.want)
		}
	}
}
