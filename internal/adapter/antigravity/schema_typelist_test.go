package antigravity

import (
	"strings"
	"testing"
)

func TestSanitizeGeminiSchemaTypeListKeepsTypesForUntypedBranches(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{
			"a sole untyped branch keeps the type list",
			`{"type":["string","number"],"KEYWORD":[{"minLength":1}]}`,
			`{"anyOf":[{"minLength":1,"type":"string"},{"minLength":1,"type":"number"}]}`,
		},
		{
			"a sole untyped branch beside null keeps the type list",
			`{"type":["string","integer","null"],"KEYWORD":[{"maxLength":4},{"type":"null"}]}`,
			`{"anyOf":[{"maxLength":4,"type":"string"},{"maxLength":4,"type":"integer"}],"nullable":true}`,
		},
		{
			"a sole untyped branch lends the array type the parent items",
			`{"type":["string","array"],"items":{"type":"string"},"KEYWORD":[{"description":"d"}]}`,
			`{"anyOf":[{"description":"d","type":"string"},{"description":"d","items":{"type":"string"},"type":"array"}]}`,
		},
		{
			"untyped branches beside a typed one are unchanged",
			`{"type":["string","integer"],"KEYWORD":[{"maxLength":3},{"type":"integer"}]}`,
			`{"anyOf":[{"maxLength":3},{"type":"integer"}]}`,
		},
	} {
		for _, keyword := range []string{"anyOf", "oneOf"} {
			t.Run(tc.name+" "+keyword, func(t *testing.T) {
				// Given a type list whose only fitting branches carry no type.
				// When reducing it to the Gemini Schema proto.
				got := sanitizedSchema(t, strings.ReplaceAll(tc.in, "KEYWORD", keyword))
				// Then every listed type survives, each with the branch's keywords.
				if got != tc.want {
					t.Fatalf("got  %s\nwant %s", got, tc.want)
				}
			})
		}
	}
}
