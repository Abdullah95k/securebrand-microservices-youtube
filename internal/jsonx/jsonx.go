// Package jsonx reads loosely structured JSON (YouTube's website responses)
// without fixed paths. YouTube moves objects around often, so parsers find
// data by object type ("videoRenderer", "commentEntityPayload"…) anywhere in
// the response instead of following one exact path (plan §8.11).
package jsonx

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Decode parses JSON keeping numbers exact (json.Number).
func Decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// Get follows a path of object keys (string) and array indexes (int). It
// returns nil when any step is missing.
func Get(v any, path ...any) any {
	cur := v
	for _, step := range path {
		switch s := step.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur = m[s]
		case int:
			a, ok := cur.([]any)
			if !ok || s < 0 || s >= len(a) {
				return nil
			}
			cur = a[s]
		default:
			return nil
		}
		if cur == nil {
			return nil
		}
	}
	return cur
}

// Map returns the object at path, or nil.
func Map(v any, path ...any) map[string]any {
	m, _ := Get(v, path...).(map[string]any)
	return m
}

// Slice returns the array at path, or nil.
func Slice(v any, path ...any) []any {
	a, _ := Get(v, path...).([]any)
	return a
}

// String returns the string at path, or "".
func String(v any, path ...any) string {
	switch s := Get(v, path...).(type) {
	case string:
		return s
	case json.Number:
		return s.String()
	}
	return ""
}

// Bool returns the boolean at path, or false.
func Bool(v any, path ...any) bool {
	b, _ := Get(v, path...).(bool)
	return b
}

// Int returns the integer at path. Strings of digits (with optional
// thousands separators) are accepted because YouTube often sends counts as
// strings.
func Int(v any, path ...any) (int64, bool) {
	switch n := Get(v, path...).(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
		if f, err := n.Float64(); err == nil {
			return int64(f), true
		}
	case float64:
		return int64(n), true
	case string:
		s := strings.NewReplacer(",", "", " ", "", " ", "", "٬", "").Replace(n)
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i, true
		}
	}
	return 0, false
}

// Text returns the display text of a YouTube text object: {"simpleText":…},
// {"runs":[{"text":…}…]} or {"content":…}. A plain string is returned as is.
func Text(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if s, ok := t["simpleText"].(string); ok {
			return s
		}
		if s, ok := t["content"].(string); ok {
			return s
		}
		if runs, ok := t["runs"].([]any); ok {
			var b strings.Builder
			for _, r := range runs {
				if s, ok := Get(r, "text").(string); ok {
					b.WriteString(s)
				}
			}
			return b.String()
		}
	}
	return ""
}

// TextAt is Text(Get(v, path...)).
func TextAt(v any, path ...any) string { return Text(Get(v, path...)) }

// FindAll returns every value stored under key anywhere in v, depth first,
// in document order.
func FindAll(v any, key string) []any {
	var out []any
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case map[string]any:
			// Visit keys in a stable order so results are deterministic.
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if k == key {
					out = append(out, t[k])
				}
				walk(t[k])
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

// FindMaps is FindAll restricted to object values.
func FindMaps(v any, key string) []map[string]any {
	var out []map[string]any
	for _, x := range FindAll(v, key) {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// First returns the first value under key anywhere in v, or nil.
func First(v any, key string) any {
	all := FindAll(v, key)
	if len(all) == 0 {
		return nil
	}
	return all[0]
}
