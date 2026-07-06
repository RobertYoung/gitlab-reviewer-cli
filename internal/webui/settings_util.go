package webui

import (
	"os"
	"sort"
	"strconv"
	"strings"
)

// mapGet reads a delimited key (e.g. "gitlab.base_url") from a nested map.
func mapGet(m map[string]any, key string) (any, bool) {
	parts := strings.Split(key, ".")
	var cur any = m
	for _, p := range parts {
		node, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = node[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// toString renders a scalar config value the way it should appear in a
// text input. Nil, slices, and maps render empty (they have their own
// controls).
func toString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

func toBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		b, _ := strconv.ParseBool(t)
		return b
	default:
		return false
	}
}

// toStringSlice coerces a config list value to []string.
func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			out = append(out, toString(e))
		}
		return out
	default:
		return nil
	}
}

// toAnySlice widens a []string for storage in the nested values map.
func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// mapLines renders a string map as sorted "key=value" lines.
func mapLines(v any) string {
	pairs := map[string]string{}
	switch t := v.(type) {
	case map[string]string:
		pairs = t
	case map[string]any:
		for k, e := range t {
			pairs[k] = toString(e)
		}
	default:
		return ""
	}
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(pairs[k])
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// formLines splits textarea input into trimmed, non-empty lines.
func formLines(s string) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// parseKeyVals turns "KEY=value" lines into a map. Lines without an '=' are
// skipped; keys are trimmed, values kept verbatim after the first '='.
func parseKeyVals(s string) map[string]any {
	out := map[string]any{}
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if k = strings.TrimSpace(k); !ok || k == "" {
			continue
		}
		out[k] = v
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
