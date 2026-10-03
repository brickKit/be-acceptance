package compconf

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// jsonPathString evaluates the small JSONPath subset the fixtures use ($, .name, [index]) and
// returns the value as text ("" when absent; numbers and booleans in their JSON form).
func jsonPathString(doc []byte, path string) string {
	var v any
	if json.Unmarshal(doc, &v) != nil {
		return ""
	}
	v, ok := jsonPath(v, path)
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func jsonPath(v any, path string) (any, bool) {
	rest := strings.TrimPrefix(path, "$")
	for rest != "" {
		switch {
		case strings.HasPrefix(rest, "."):
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			m, ok := v.(map[string]any)
			if !ok {
				return nil, false
			}
			if v, ok = m[rest[:end]]; !ok {
				return nil, false
			}
			rest = rest[end:]
		case strings.HasPrefix(rest, "["):
			end := strings.Index(rest, "]")
			var i int
			if end < 0 || func() error { _, err := fmt.Sscanf(rest[1:end], "%d", &i); return err }() != nil {
				return nil, false
			}
			a, ok := v.([]any)
			if !ok || i < 0 || i >= len(a) {
				return nil, false
			}
			v, rest = a[i], rest[end+1:]
		default:
			return nil, false
		}
	}
	return v, true
}
