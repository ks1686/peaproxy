package clients

import (
	"bytes"
	"encoding/json"
	"strings"
)

// upsertJSONKey sets key to value, keeping an existing key at its position and
// appending a new one at the end.
func upsertJSONKey(body []byte, key string, value any) ([]byte, error) {
	var encoded []byte
	if raw, ok := value.(json.RawMessage); ok {
		// A RawMessage already holds the bytes to write. Marshalling it would
		// compact it, and with it the layout of the object it carries, which is
		// why rewriting a nested key used to need a whole-document re-indent to
		// put the file back.
		encoded = bytes.TrimSpace(raw)
	} else {
		var err error
		if encoded, err = json.Marshal(value); err != nil {
			return nil, err
		}
	}
	return rewriteJSONKey(body, key, encoded)
}

func getJSONKey(body []byte, key string) ([]byte, bool) {
	if len(body) == 0 || body[0] != '{' {
		return nil, false
	}
	i := 1
	for {
		i = skipWS(body, i)
		if i >= len(body) || body[i] != '"' {
			return nil, false
		}
		nameStart := i
		i = scanString(body, i)
		name := body[nameStart:i]
		i = skipWS(body, i)
		if i >= len(body) || body[i] != ':' {
			return nil, false
		}
		i++
		valueStart := skipWS(body, i)
		i = scanValue(body, valueStart)
		if string(name) == `"`+key+`"` {
			return bytes.TrimSpace(body[valueStart:i]), true
		}
		i = skipWS(body, i)
		if i < len(body) && body[i] == ',' {
			i++
		}
	}
}

func deleteJSONKey(body []byte, key string) ([]byte, error) {
	return rewriteJSONKey(body, key, nil)
}

// jsonPair is one key and its raw value, as it appears in the file. layout
// marks a value PeaProxy just wrote into a multi-line object that had no
// content of its own to preserve, so it is laid out like its neighbours.
type jsonPair struct {
	name, value []byte
	layout      bool
}

// rewriteJSONKey replaces the first occurrence of key with replacement (dropping
// any duplicates), appending it when key is absent. A nil replacement deletes
// key. key must be a plain ASCII JSON name; it is not escaped.
//
// An object that was written across several lines stays that way, one key per
// line at its own indentation, and every value it does not touch is copied
// byte for byte. That is what keeps a connect/disconnect round trip from
// rewriting the parts of the file PeaProxy has no business reformatting.
func rewriteJSONKey(body []byte, key string, replacement []byte) ([]byte, error) {
	if len(body) == 0 || body[0] != '{' {
		return nil, errorsNewJSON()
	}
	var pairs []jsonPair
	i := 1
	found := false
	for {
		i = skipWS(body, i)
		if i >= len(body) {
			return nil, errorsNewJSON()
		}
		if body[i] == '}' {
			if !found && replacement != nil {
				pairs = append(pairs, jsonPair{[]byte(`"` + key + `"`), replacement, true})
			}
			return renderObject(body, pairs), nil
		}
		if body[i] != '"' {
			return nil, errorsNewJSON()
		}
		nameStart := i
		i = scanString(body, i)
		name := body[nameStart:i]
		i = skipWS(body, i)
		if i >= len(body) || body[i] != ':' {
			return nil, errorsNewJSON()
		}
		i++
		valueStart := i
		i = scanValue(body, i)
		value := body[valueStart:i]
		i = skipWS(body, i)
		if i < len(body) && body[i] == ',' {
			i++
		}
		if string(name) == `"`+key+`"` {
			if !found && replacement != nil {
				// An object that was there but empty has no formatting of its
				// own to keep, so the replacement is laid out like the rest.
				pairs = append(pairs, jsonPair{name, replacement, isEmptyObject(value)})
			}
			found = true
			continue
		}
		pairs = append(pairs, jsonPair{name, bytes.TrimSpace(value), false})
	}
}

func isEmptyObject(value []byte) bool {
	value = bytes.TrimSpace(value)
	return string(value) == "{}" || string(value) == "{\n}" || string(value) == "{\r\n}"
}

// renderObject writes pairs back in the layout body already used: one key per
// line at the body's own indentation when it was multi-line, and on a single
// line when it was not.
func renderObject(body []byte, pairs []jsonPair) []byte {
	indent, multiline := detectIndent(body)
	if !multiline {
		return renderInline(body, pairs)
	}
	if len(pairs) == 0 {
		return []byte("{}")
	}
	// A nested object closes on the line its own key is on, which is one
	// indentation step in from its children. The body's own last line says so.
	closing := bodyIndentAt(body, bytes.LastIndexByte(body, '\n')+1)
	out := make([]byte, 0, len(body)+len(pairs)*(len(indent)+8))
	out = append(out, '{', '\n')
	unit := strings.TrimPrefix(indent, string(closing))
	if unit == "" {
		unit = indent
	}
	for n, p := range pairs {
		if n > 0 {
			out = append(out, ',', '\n')
		}
		out = append(out, indent...)
		out = append(out, p.name...)
		out = append(out, ':', ' ')
		if p.layout {
			// The new object sits one step in from the keys around it.
			out = append(out, layOutObject(p.value, []byte(indent+unit), []byte(indent))...)
			continue
		}
		out = append(out, p.value...)
	}
	out = append(out, '\n')
	out = append(out, closing...)
	out = append(out, '}')
	return out
}

// layOutObject spreads an object PeaProxy just created across the lines of the
// object holding it, so a new entry in a file written over several lines is not
// the one line on it. Arrays inside it are left exactly as they are.
func layOutObject(value []byte, indent, closing []byte) []byte {
	if len(value) == 0 || value[0] != '{' {
		return value
	}
	pairs, ok := topLevelPairs(value)
	if !ok || len(pairs) == 0 {
		return value
	}
	out := make([]byte, 0, len(value)+len(pairs)*(len(indent)+8))
	out = append(out, '{', '\n')
	for n, p := range pairs {
		if n > 0 {
			out = append(out, ',', '\n')
		}
		out = append(out, indent...)
		out = append(out, p.name...)
		out = append(out, ':', ' ')
		if len(p.value) > 0 && p.value[0] == '{' && !bytes.Contains(p.value, []byte("\n")) {
			out = append(out, layOutObject(p.value, append(append([]byte{}, indent...), indent...), indent)...)
		} else {
			out = append(out, p.value...)
		}
	}
	out = append(out, '\n')
	out = append(out, closing...)
	out = append(out, '}')
	return out
}

func topLevelPairs(body []byte) ([]jsonPair, bool) {
	if len(body) == 0 || body[0] != '{' {
		return nil, false
	}
	var pairs []jsonPair
	i := 1
	for {
		i = skipWS(body, i)
		if i >= len(body) {
			return nil, false
		}
		if body[i] == '}' {
			return pairs, true
		}
		if body[i] != '"' {
			return nil, false
		}
		nameStart := i
		i = scanString(body, i)
		name := body[nameStart:i]
		i = skipWS(body, i)
		if i >= len(body) || body[i] != ':' {
			return nil, false
		}
		i++
		valueStart := i
		i = scanValue(body, i)
		if i > len(body) {
			return nil, false
		}
		pairs = append(pairs, jsonPair{name, bytes.TrimSpace(body[valueStart:i]), false})
		i = skipWS(body, i)
		if i < len(body) && body[i] == ',' {
			i++
		}
	}
}

// renderInline writes an object that was on a single line back on one line, in
// the same spacing: a user who wrote {"a": "b"} gets that back, not {"a":"b"}.
func renderInline(body []byte, pairs []jsonPair) []byte {
	colonGap, braceGap := inlineSpacing(body)
	colonSep, commaSep := []byte(":"), []byte(",")
	if colonGap {
		colonSep, commaSep = []byte(": "), []byte(", ")
	}
	brace := []byte(nil)
	if braceGap {
		brace = body[1:2]
	}
	out := []byte{'{'}
	if len(pairs) == 0 {
		return append(out, '}')
	}
	out = append(out, brace...)
	for n, p := range pairs {
		if n > 0 {
			out = append(out, commaSep...)
		}
		out = append(out, p.name...)
		out = append(out, colonSep...)
		out = append(out, p.value...)
	}
	out = append(out, brace...)
	return append(out, '}')
}

// inlineSpacing reports whether a single-line object spaced its pairs after the
// colon, and whether it padded inside its braces. The two are independent:
// {"a": "b"} and { "a":"b" } are both things a user typed.
func inlineSpacing(body []byte) (colonGap, braceGap bool) {
	for i := 1; i < len(body); i++ {
		switch body[i] {
		case '"':
			i = scanString(body, i) - 1
		case ':':
			colonGap = i+1 < len(body) && (body[i+1] == ' ' || body[i+1] == '\t')
			i = len(body)
		}
	}
	braceGap = len(body) > 1 && (body[1] == ' ' || body[1] == '\t')
	return colonGap, braceGap
}

// bodyIndentAt is the run of spaces and tabs body starts with at the given
// offset, which is where the closing brace of that line sits.
func bodyIndentAt(body []byte, at int) []byte {
	if at < 0 || at > len(body) {
		return nil
	}
	line := body[at:]
	for i := 0; i < len(line); i++ {
		if line[i] != ' ' && line[i] != '\t' {
			return line[:i]
		}
	}
	return line
}

func errorsNewJSON() error {
	return errJSON
}

var errJSON = errString("client config is not json")

type errString string

func (e errString) Error() string { return string(e) }

func skipWS(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	return i
}

func scanString(b []byte, i int) int {
	i++
	for i < len(b) {
		if b[i] == '\\' {
			i += 2
			continue
		}
		if b[i] == '"' {
			return i + 1
		}
		i++
	}
	return i
}

func scanValue(b []byte, i int) int {
	i = skipWS(b, i)
	if i >= len(b) {
		return i
	}
	switch b[i] {
	case '"':
		return scanString(b, i)
	case '{', '[':
		open := b[i]
		close := byte('}')
		if open == '[' {
			close = ']'
		}
		depth := 0
		for i < len(b) {
			if b[i] == '"' {
				i = scanString(b, i)
				continue
			}
			if b[i] == open {
				depth++
				i++
				continue
			}
			if b[i] == close {
				depth--
				i++
				if depth == 0 {
					return i
				}
				continue
			}
			i++
		}
		return i
	default:
		for i < len(b) && b[i] != ',' && b[i] != '}' && b[i] != ']' {
			i++
		}
		return i
	}
}

// detectIndent reports the indent unit of a JSON object body: the leading
// whitespace of the first indented line after the opening brace. multiline is
// false for single-line bodies, which are written back compact.
func detectIndent(body []byte) (indent string, multiline bool) {
	body = bytes.TrimSpace(body)
	if bytes.IndexByte(body, '\n') < 0 {
		return "", false
	}
	for _, line := range bytes.Split(body, []byte("\n"))[1:] {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) != 0 && len(trimmed) != len(line) {
			return string(line[:len(line)-len(trimmed)]), true
		}
	}
	return "", true
}
