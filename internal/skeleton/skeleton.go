// Package skeleton turns a JSON document into its structure only: every
// value becomes a type placeholder and arrays keep one example element.
// Phase 0 saves skeletons of responses the parsers could not read, so the
// structure can be reviewed and shared without any real data (plan §8.10).
package skeleton

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
)

// Placeholders used for values.
const (
	String = "<string>"
	Number = "<number>"
	Bool   = "<bool>"
	Null   = "<null>"
	// Key replaces object keys that look like data (IDs, long tokens)
	// rather than field names.
	Key = "<key>"
)

// A field name is short and made of letters, digits and underscores, the way
// YouTube's schema names look ("videoRenderer", "simpleText"). Anything else
// (IDs used as keys, tokens, text) is replaced.
var fieldName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// Of returns the skeleton of v (a value from encoding/json or jsonx.Decode).
func Of(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			name := k
			if !fieldName.MatchString(k) || looksLikeID(k) {
				name = Key
			}
			if _, dup := out[name]; dup {
				continue // keep one example per placeholder key
			}
			out[name] = Of(t[k])
		}
		return out
	case []any:
		if len(t) == 0 {
			return []any{}
		}
		// Keep one example of each distinct element shape, so lists that mix
		// renderer types show every type.
		var out []any
		seen := map[string]bool{}
		for _, e := range t {
			s := Of(e)
			sig, _ := json.Marshal(shape(s))
			if seen[string(sig)] {
				continue
			}
			seen[string(sig)] = true
			out = append(out, s)
		}
		return out
	case string:
		return String
	case json.Number, float64, int, int64:
		return Number
	case bool:
		return Bool
	case nil:
		return Null
	}
	return Null
}

// shape keeps only the top-level keys of objects, to group array elements.
func shape(v any) any {
	if m, ok := v.(map[string]any); ok {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}
	return v
}

// looksLikeID catches keys such as video IDs (11 URL-safe characters with
// mixed case and digits) used as object keys.
func looksLikeID(k string) bool {
	if len(k) < 10 {
		return false
	}
	hasDigit, hasUpper, hasLower := false, false, false
	for _, r := range k {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		}
	}
	return hasDigit && hasUpper && hasLower
}

// Bytes parses JSON and returns its skeleton, indented.
func Bytes(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep "<string>" readable
	enc.SetIndent("", "  ")
	if err := enc.Encode(Of(v)); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
