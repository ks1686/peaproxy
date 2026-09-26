package jsonx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// Peek is a typed extract from a client body. Unmarshal into structs is safe;
// never round-trip request JSON through map[string]any (VibeProxy #292).
type Peek struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

// PeekBody reads model/stream without reserializing the rest of the document.
func PeekBody(raw []byte) Peek {
	var p Peek
	_ = json.Unmarshal(raw, &p)
	return p
}

var streamKey = regexp.MustCompile(`"stream"\s*:\s*(true|false)`)

// SetStream surgically patches the stream flag, preserving key order and all
// other bytes. If the key is missing it is inserted immediately before the
// final closing brace. Empty input returns a tiny object.
func SetStream(raw []byte, stream bool) []byte {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	lit := "false"
	if stream {
		lit = "true"
	}
	if streamKey.Match(raw) {
		return streamKey.ReplaceAll(raw, []byte(`"stream":`+lit))
	}
	end := bytes.LastIndexByte(raw, '}')
	if end < 0 {
		return raw
	}
	inner := bytes.TrimSpace(raw[:end])
	insert := `"stream":` + lit
	if len(inner) > 1 && inner[len(inner)-1] != '{' {
		insert = "," + insert
	}
	out := make([]byte, 0, len(raw)+len(insert)+1)
	out = append(out, inner...)
	out = append(out, []byte(insert)...)
	out = append(out, raw[end:]...)
	return out
}

// Quote is a JSON string literal.
func Quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return strconv.Quote(s)
	}
	return string(b)
}

// DecodeStrict unmarshals into a struct. Callers must not use maps for bodies
// that will be forwarded to Anthropic.
func DecodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("json: %w", err)
	}
	return nil
}
