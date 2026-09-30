package antigravity

import (
	"encoding/json"
	"reflect"
	"testing"
)

func mustJSONMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// #55: typedCopies copied every key of an untyped branch onto every per-type
// copy, so a string branch ended up with `minimum` and a number branch with
// `minLength`.
func TestTypedCopiesFilterKeywordsByType(t *testing.T) {
	branches := []any{
		map[string]any{
			"type":        []any{"string", "number"},
			"minLength":   1,
			"maximum":     10,
			"pattern":     "^a",
			"minimum":     0,
			"enum":        []any{"a", 1},
			"description": "shared",
			"nullable":    true,
			"title":       "T",
			"default":     "a",
			"const":       "a",
		},
	}
	got := typedCopies(branches, []any{"string", "number"}, nil, false)
	if len(got) != 2 {
		t.Fatalf("got %d copies, want 2: %s", len(got), mustJSONMap(t, got))
	}
	str := mustJSONMap(t, got[0])
	num := mustJSONMap(t, got[1])

	// String-only keywords.
	for _, k := range []string{"minLength", "pattern"} {
		if _, ok := str[k]; !ok {
			t.Errorf("string copy lost %q: %v", k, str)
		}
		if _, ok := num[k]; ok {
			t.Errorf("number copy has string keyword %q: %v", k, num)
		}
	}
	// Number-only keywords.
	for _, k := range []string{"minimum", "maximum"} {
		if _, ok := num[k]; !ok {
			t.Errorf("number copy lost %q: %v", k, num)
		}
		if _, ok := str[k]; ok {
			t.Errorf("string copy has number keyword %q: %v", k, str)
		}
	}
	// Shared keywords stay on both.
	for _, k := range []string{"enum", "description", "nullable", "title", "default", "const"} {
		if _, ok := str[k]; !ok {
			t.Errorf("string copy lost shared keyword %q: %v", k, str)
		}
		if _, ok := num[k]; !ok {
			t.Errorf("number copy lost shared keyword %q: %v", k, num)
		}
	}
	if str["type"] != "string" || num["type"] != "number" {
		t.Errorf("types are wrong: %v %v", str["type"], num["type"])
	}
}

// Gemini accepts `format` on a number as well, so it is not string-only.
func TestFormatStaysOnBothStringAndNumber(t *testing.T) {
	branches := []any{map[string]any{"type": []any{"string", "number"}, "format": "int32"}}
	got := typedCopies(branches, []any{"string", "number"}, nil, false)
	for _, b := range got {
		m := mustJSONMap(t, b)
		if m["format"] != "int32" {
			t.Errorf("%v copy lost format: %v", m["type"], m)
		}
	}
}

func TestTypedCopiesFilterArrayAndObjectKeywords(t *testing.T) {
	branches := []any{
		map[string]any{
			"type":                 []any{"array", "object"},
			"items":                map[string]any{"type": "string"},
			"minItems":             1,
			"properties":           map[string]any{"a": map[string]any{"type": "string"}},
			"additionalProperties": false,
			"minLength":            3,
		},
	}
	got := typedCopies(branches, []any{"array", "object"}, nil, false)
	arr := mustJSONMap(t, got[0])
	obj := mustJSONMap(t, got[1])

	for _, k := range []string{"items", "minItems"} {
		if _, ok := arr[k]; !ok {
			t.Errorf("array copy lost %q: %v", k, arr)
		}
		if _, ok := obj[k]; ok {
			t.Errorf("object copy has array keyword %q: %v", k, obj)
		}
	}
	for _, k := range []string{"properties", "additionalProperties"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("object copy lost %q: %v", k, obj)
		}
		if _, ok := arr[k]; ok {
			t.Errorf("array copy has object keyword %q: %v", k, arr)
		}
	}
	if _, ok := arr["minLength"]; ok {
		t.Errorf("array copy has a string keyword: %v", arr)
	}
}

// An array branch with no items of its own takes the parent's, and that has to
// happen after the filter rather than before it.
func TestTypedCopiesGivesTheArrayBranchTheParentsItems(t *testing.T) {
	branches := []any{map[string]any{"type": []any{"array"}}}
	items := map[string]any{"type": "string", "minLength": 2}
	got := typedCopies(branches, []any{"array"}, items, true)
	if len(got) != 1 {
		t.Fatalf("got %d copies, want 1", len(got))
	}
	if !reflect.DeepEqual(mustJSONMap(t, got[0])["items"], map[string]any{"type": "string", "minLength": 2.0}) {
		t.Errorf("the array branch did not get the parent's items: %v", got[0])
	}
}

// A null branch is not typed and must not be copied.
func TestTypedCopiesLeavesNullBranchesAlone(t *testing.T) {
	nullBranch := map[string]any{"type": "null"}
	got := typedCopies([]any{nullBranch}, []any{"string", "number"}, nil, false)
	if len(got) != 1 {
		t.Fatalf("got %d copies, want 1: %v", len(got), got)
	}
	if !reflect.DeepEqual(got[0], nullBranch) {
		t.Errorf("the null branch was rewritten: %v", got[0])
	}
}

// A keyword the filter does not know about must survive, or a valid schema
// would silently lose constraints we have not enumerated.
func TestTypedCopiesKeepsUnknownKeywords(t *testing.T) {
	got := typedCopies([]any{map[string]any{"type": []any{"string"}, "x-vendor": "keep"}}, []any{"string"}, nil, false)
	m := mustJSONMap(t, got[0])
	if m["x-vendor"] != "keep" {
		t.Errorf("an unrecognised keyword was dropped: %v", m)
	}
}
