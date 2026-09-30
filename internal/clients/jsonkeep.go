package clients

import (
	"bytes"
	"encoding/json"
)

// upsertJSONKey sets key to value, keeping an existing key at its position and
// appending a new one at the end.
func upsertJSONKey(body []byte, key string, value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
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

// rewriteJSONKey compacts body and replaces the first occurrence of key with
// replacement (dropping any duplicates), appending it when key is absent. A
// nil replacement deletes key. key must be a plain ASCII JSON name; it is not escaped.
func rewriteJSONKey(body []byte, key string, replacement []byte) ([]byte, error) {
	if len(body) == 0 || body[0] != '{' {
		return nil, errorsNewJSON()
	}
	var out []byte
	out = append(out, '{')
	i := 1
	first := true
	found := false
	emit := func(name, value []byte) {
		if !first {
			out = append(out, ',')
		}
		first = false
		out = append(out, name...)
		out = append(out, ':')
		out = append(out, value...)
	}
	for {
		i = skipWS(body, i)
		if i >= len(body) {
			return nil, errorsNewJSON()
		}
		if body[i] == '}' {
			if !found && replacement != nil {
				emit([]byte(`"`+key+`"`), replacement)
			}
			out = append(out, '}')
			return out, nil
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
				emit(name, replacement)
			}
			found = true
			continue
		}
		emit(name, bytes.TrimSpace(value))
	}
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
