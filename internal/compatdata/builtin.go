package compatdata

import _ "embed"

//go:embed builtin.json
var builtinJSON []byte

// Bundled is the known-good profile snapshot shipped with the binary.
func Bundled() Document {
	doc, err := Parse(builtinJSON)
	if err != nil {
		return Document{Schema: SchemaVersion, Profiles: map[string]Profile{}}
	}
	return doc
}
