package clients

import (
	"bytes"
	"encoding/json"
)

func upsertJSONKey(body []byte, key string, value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	without, err := deleteJSONKey(body, key)
	if err != nil {
		return nil, err
	}
	end := bytes.LastIndexByte(without, '}')
	if end < 0 {
		return nil, errorsNewJSON()
	}
	member := append([]byte(`"`+key+`":`), encoded...)
	inner := bytes.TrimSpace(without[:end])
	if len(inner) == 0 || inner[len(inner)-1] == '{' {
		return append(append(without[:end], member...), without[end:]...), nil
	}
	return append(append(without[:end], append([]byte{','}, member...)...), without[end:]...), nil
}

func deleteJSONKey(body []byte, key string) ([]byte, error) {
	if len(body) == 0 || body[0] != '{' {
		return nil, errorsNewJSON()
	}
	var out []byte
	out = append(out, '{')
	i := 1
	first := true
	for {
		i = skipWS(body, i)
		if i >= len(body) {
			return nil, errorsNewJSON()
		}
		if body[i] == '}' {
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
			continue
		}
		if !first {
			out = append(out, ',')
		}
		first = false
		out = append(out, name...)
		out = append(out, ':')
		out = append(out, bytes.TrimSpace(value)...)
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
