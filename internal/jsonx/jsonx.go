package jsonx

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// SetStream surgically patches the top-level stream flag, preserving key order
// and all other bytes (including nested "stream" keys and quoted text).
func SetStream(raw []byte, stream bool) []byte {
	return SetBool(raw, "stream", stream)
}

// SetBool surgically patches a top-level boolean key, preserving key order
// and all other bytes (including nested keys and quoted text).
// If the key is already the requested bool, the input is returned unchanged.
// If the key is missing it is inserted immediately before the final closing brace.
func SetBool(raw []byte, key string, val bool) []byte {
	if key == "" {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			return []byte(`{}`)
		}
		return raw
	}
	lit := []byte("false")
	if val {
		lit = []byte("true")
	}
	return SetTopLevelRaw(raw, key, lit)
}

// SetTopLevelRaw inserts or replaces a top-level object member with a raw JSON
// value, preserving the order of every other key (prompt-cache safe).
func SetTopLevelRaw(raw []byte, key string, value []byte) []byte {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	if key == "" {
		return raw
	}
	for _, s := range topLevelMembers(raw) {
		if s.key != key {
			continue
		}
		if bytes.Equal(raw[s.valueStart:s.valueEnd], value) {
			return raw
		}
		out := make([]byte, 0, len(raw)-s.valueEnd+s.valueStart+len(value))
		out = append(out, raw[:s.valueStart]...)
		out = append(out, value...)
		out = append(out, raw[s.valueEnd:]...)
		return out
	}
	end := lastTopLevelClose(raw)
	if end < 0 {
		return raw
	}
	inner := bytes.TrimRight(raw[:end], " \t\r\n")
	insert := make([]byte, 0, 3+len(key)+len(value))
	insert = append(insert, '"')
	insert = append(insert, key...)
	insert = append(insert, '"', ':')
	insert = append(insert, value...)
	if len(inner) > 1 && inner[len(inner)-1] != '{' {
		insert = append([]byte{','}, insert...)
	}
	out := make([]byte, 0, len(raw)+len(insert))
	out = append(out, inner...)
	out = append(out, insert...)
	out = append(out, raw[end:]...)
	return out
}

// DropKeyInArray removes dropKey from each object element of the top-level
// array named arrayKey. Other keys stay in place. A missing array is a no-op.
func DropKeyInArray(raw []byte, arrayKey, dropKey string) []byte {
	raw = bytes.TrimSpace(raw)
	if arrayKey == "" || dropKey == "" {
		return raw
	}
	for _, s := range topLevelMembers(raw) {
		if s.key != arrayKey {
			continue
		}
		val := bytes.TrimSpace(raw[s.valueStart:s.valueEnd])
		next := dropKeyFromArray(val, dropKey)
		if bytes.Equal(val, next) {
			return raw
		}
		out := make([]byte, 0, len(raw)-len(val)+len(next))
		out = append(out, raw[:s.valueStart]...)
		out = append(out, next...)
		out = append(out, raw[s.valueEnd:]...)
		return out
	}
	return raw
}

func dropKeyFromArray(raw []byte, dropKey string) []byte {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return raw
	}
	var b bytes.Buffer
	b.WriteByte('[')
	i := 1
	first := true
	changed := false
	for i < len(raw) {
		i = skipSpace(raw, i)
		if i >= len(raw) || raw[i] == ']' {
			break
		}
		if raw[i] == ',' {
			i++
			continue
		}
		end, ok := skipValue(raw, i)
		if !ok {
			return raw
		}
		elem := bytes.TrimSpace(raw[i:end])
		if len(elem) > 0 && elem[0] == '{' {
			stripped := DropTopLevelKeys(elem, dropKey)
			if !bytes.Equal(stripped, elem) {
				changed = true
				elem = stripped
			}
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.Write(elem)
		i = end
	}
	if !changed {
		return raw
	}
	b.WriteByte(']')
	return b.Bytes()
}

// DropTopLevelKeys removes named top-level object keys without reordering
// remaining fields. Unknown keys are ignored. Used to strip Amp/Codex
// stream_options (and similar) that chatgpt.com/codex rejects.
func DropTopLevelKeys(raw []byte, keys ...string) []byte {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(keys) == 0 {
		return raw
	}
	want := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		want[k] = struct{}{}
	}
	spans := topLevelMembers(raw)
	if len(spans) == 0 {
		return raw
	}
	var b bytes.Buffer
	b.Grow(len(raw))
	pos := 0
	dropped := false
	for i, s := range spans {
		if _, ok := want[s.key]; !ok {
			continue
		}
		dropped = true
		start, end := memberCut(raw, spans, i)
		b.Write(raw[pos:start])
		pos = end
	}
	if !dropped {
		return raw
	}
	b.Write(raw[pos:])
	return b.Bytes()
}

// memberCut returns the byte range to delete for spans[i], including one
// adjacent comma so the remaining object stays valid.
func memberCut(raw []byte, spans []memberSpan, i int) (start, end int) {
	s := spans[i]
	start, end = s.keyStart, s.valueEnd
	if i > 0 {
		// Drop the comma between the previous member and this one.
		prev := spans[i-1].valueEnd
		return prev, end
	}
	// First member: drop a following comma if present.
	j := skipSpace(raw, end)
	if j < len(raw) && raw[j] == ',' {
		end = j + 1
	}
	return start, end
}

type memberSpan struct {
	key        string
	keyStart   int // index of opening quote of the key
	valueStart int
	valueEnd   int // exclusive
}

func topLevelMembers(raw []byte) []memberSpan {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var out []memberSpan
	i := 1
	n := len(raw)
	for i < n {
		i = skipSpace(raw, i)
		if i >= n {
			break
		}
		if raw[i] == '}' {
			break
		}
		if raw[i] == ',' {
			i++
			continue
		}
		if raw[i] != '"' {
			return out
		}
		keyStart := i
		key, next, ok := parseString(raw, i)
		if !ok {
			return out
		}
		i = skipSpace(raw, next)
		if i >= n || raw[i] != ':' {
			return out
		}
		i = skipSpace(raw, i+1)
		valueStart := i
		valueEnd, ok := skipValue(raw, i)
		if !ok {
			return out
		}
		out = append(out, memberSpan{
			key:        key,
			keyStart:   keyStart,
			valueStart: valueStart,
			valueEnd:   valueEnd,
		})
		i = valueEnd
	}
	return out
}

func lastTopLevelClose(raw []byte) int {
	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 && c == '}' {
				return i
			}
		}
	}
	return bytes.LastIndexByte(raw, '}')
}

func skipSpace(raw []byte, i int) int {
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\n' || raw[i] == '\r') {
		i++
	}
	return i
}

func parseString(raw []byte, i int) (string, int, bool) {
	if i >= len(raw) || raw[i] != '"' {
		return "", i, false
	}
	j := i + 1
	esc := false
	for j < len(raw) {
		c := raw[j]
		if esc {
			esc = false
			j++
			continue
		}
		if c == '\\' {
			esc = true
			j++
			continue
		}
		if c == '"' {
			var s string
			if err := json.Unmarshal(raw[i:j+1], &s); err != nil {
				return "", j + 1, false
			}
			return s, j + 1, true
		}
		j++
	}
	return "", j, false
}

func skipValue(raw []byte, i int) (int, bool) {
	i = skipSpace(raw, i)
	if i >= len(raw) {
		return i, false
	}
	switch raw[i] {
	case '"':
		_, next, ok := parseString(raw, i)
		return next, ok
	case '{', '[':
		end, ok := skipDelimited(raw, i)
		return end, ok
	case 't':
		return skipLiteral(raw, i, "true")
	case 'f':
		return skipLiteral(raw, i, "false")
	case 'n':
		return skipLiteral(raw, i, "null")
	default:
		return skipNumber(raw, i)
	}
}

func skipDelimited(raw []byte, i int) (int, bool) {
	if i >= len(raw) {
		return i, false
	}
	open := raw[i]
	var close byte
	switch open {
	case '{':
		close = '}'
	case '[':
		close = ']'
	default:
		return i, false
	}
	depth := 0
	inStr := false
	esc := false
	for j := i; j < len(raw); j++ {
		c := raw[j]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return j + 1, true
			}
		}
	}
	return i, false
}

func skipLiteral(raw []byte, i int, lit string) (int, bool) {
	if i+len(lit) > len(raw) {
		return i, false
	}
	if string(raw[i:i+len(lit)]) != lit {
		return i, false
	}
	return i + len(lit), true
}

func skipNumber(raw []byte, i int) (int, bool) {
	if i >= len(raw) {
		return i, false
	}
	start := i
	if raw[i] == '-' {
		i++
	}
	for i < len(raw) && ((raw[i] >= '0' && raw[i] <= '9') || raw[i] == '.' || raw[i] == 'e' || raw[i] == 'E' || raw[i] == '+' || raw[i] == '-') {
		i++
	}
	return i, i > start
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
