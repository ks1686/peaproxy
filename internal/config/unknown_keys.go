package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// UnknownKey is one key a config file carries that the loader does not read.
//
// A key nobody reads is not a typo the file can explain: the setting it was
// meant to set keeps its default, and the default is usually silence. The
// commonest case is a case difference -- baseUrl for baseURL -- which no
// amount of careful reading would ever notice.
type UnknownKey struct {
	Path string
	Key  string
	// Nearest is the closest key that does exist, when one is close enough to
	// be worth naming. Empty when nothing resembles it.
	Nearest string
}

// yamlFields maps the YAML name of every field a struct actually decodes to
// its field index. Only tagged fields count: an untagged field is one the
// loader never reads either way.
func yamlFields(t reflect.Type) map[string]int {
	out := map[string]int{}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return out
	}
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			continue
		}
		out[name] = i
	}
	return out
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// walkUnknown compares one decoded YAML node against the struct it would have
// been decoded into, descending through nested structs and lists of records.
func walkUnknown(t reflect.Type, node any, path string, out *[]UnknownKey) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	fields := yamlFields(t)

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys) // stable reporting order

	for _, k := range keys {
		v := m[k]
		idx, known := fields[k]
		if !known {
			*out = append(*out, UnknownKey{
				Path:    path,
				Key:     k,
				Nearest: nearestField(fields, k),
			})
			continue
		}
		ft := t.Field(idx).Type
		switch ft.Kind() {
		case reflect.Struct:
			walkUnknown(ft, v, joinPath(path, k), out)
		case reflect.Pointer:
			walkUnknown(ft, v, joinPath(path, k), out)
		case reflect.Slice:
			// A list of records, e.g. providers. Every element is checked
			// against the same struct, addressed by index so two accounts
			// carrying the same mistake do not collapse into one report.
			items, ok := v.([]any)
			if !ok {
				continue
			}
			elem := ft.Elem()
			for i, item := range items {
				walkUnknown(elem, item, fmt.Sprintf("%s[%d]", joinPath(path, k), i), out)
			}
		}
	}
}

// nearestField names the closest key that does exist, but only when it is close
// enough that naming it helps. Pairing a typo with something unrelated would be
// worse than admitting there is no match.
func nearestField(fields map[string]int, key string) string {
	best, bestScore := "", 0
	for name := range fields {
		if s := similarity(key, name); s > bestScore {
			best, bestScore = name, s
		}
	}
	// Three shared characters in a row, or the whole shorter key, is enough to
	// be a real hint; below that the resemblance is imagined.
	if bestScore < 3 {
		return ""
	}
	return best
}

// similarity is a case-insensitive longest-common-substring score, which suits
// config keys: they are short, and a typo usually keeps most of its letters.
func similarity(a, b string) int {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	best := 0
	for i := range la {
		for j := range lb {
			n := 0
			for i+n < len(la) && j+n < len(lb) && la[i+n] == lb[j+n] {
				n++
			}
			if n > best {
				best = n
			}
		}
	}
	return best
}

// UnknownKeys reports keys present in a raw config that the loader ignores.
//
// It exists because PeaProxy cannot fail on them yet: a config carrying one is
// a working config today, and rejecting it would break a running deployment
// over a spelling. So this reports rather than refuses, and the caller decides
// how loudly to say it. Once the configs in the wild are clean, the same list
// can become the error it should have been from the start.
func UnknownKeys(raw []byte) ([]UnknownKey, error) {
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var out []UnknownKey
	walkUnknown(reflect.TypeOf(Config{}), doc, "", &out)
	return out, nil
}
