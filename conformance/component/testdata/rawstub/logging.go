package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Logger writes one JSON object per line on stdout (P18.2): envelope fields first, personal
// data redacted by key, at most 2 KiB per line.
type Logger struct {
	mu      sync.Mutex
	out     io.Writer
	min     int
	id, ver string
}

var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// envelopeKeys are never redacted or truncated, and are written first, in this order.
var envelopeKeys = []string{"time", "level", "msg", "component_id", "component_version", "trace_id", "span_id", "request_id"}

const maxLine = 2048

func newLogger(level, id, ver string) *Logger {
	r, ok := levelRank[level]
	if !ok {
		r = 1
	}
	return &Logger{out: os.Stdout, min: r, id: id, ver: ver}
}

// F is the fields of one record.
type F map[string]any

func (l *Logger) Debug(msg string, f F) { l.log("debug", msg, f) }
func (l *Logger) Info(msg string, f F)  { l.log("info", msg, f) }
func (l *Logger) Warn(msg string, f F)  { l.log("warn", msg, f) }
func (l *Logger) Error(msg string, f F) { l.log("error", msg, f) }

// Log writes at a level chosen at run time; "none" writes nothing.
func (l *Logger) Log(level, msg string, f F) {
	if level != "none" {
		l.log(level, msg, f)
	}
}

func (l *Logger) log(level, msg string, f F) {
	if levelRank[level] < l.min {
		return
	}
	rec := map[string]any{}
	for k, v := range f {
		rec[k] = v
	}
	rec["time"] = time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
	rec["level"] = level
	rec["msg"] = msg
	rec["component_id"] = l.id
	rec["component_version"] = l.ver
	if broken != "no-redact" { // broken variant no-redact: personal data reaches the log
		rec = redactRecord(rec)
	}
	line := encodeLine(rec)
	l.mu.Lock()
	_, _ = l.out.Write(append(line, '\n'))
	l.mu.Unlock()
}

func isEnvelope(k string) bool {
	for _, e := range envelopeKeys {
		if e == k {
			return true
		}
	}
	return false
}

// redactRecord applies the redaction rule to every field outside the envelope.
func redactRecord(rec map[string]any) map[string]any {
	out := make(map[string]any, len(rec))
	for k, v := range rec {
		if isEnvelope(k) {
			out[k] = v
			continue
		}
		if protectedKey(k) {
			out[k] = "[REDACTED]"
			continue
		}
		out[k] = redactValue(v)
	}
	return out
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			if protectedKey(k) {
				m[k] = "[REDACTED]"
			} else {
				m[k] = redactValue(x)
			}
		}
		return m
	case F:
		return redactValue(map[string]any(t))
	case []any:
		a := make([]any, len(t))
		for i, x := range t {
			a[i] = redactValue(x)
		}
		return a
	}
	return v
}

var protectedNames = [][]string{
	{"phone"}, {"mobile"}, {"id", "card"}, {"password"}, {"bank", "card"}, {"email"}, {"token"},
	{"secret"}, {"authorization"}, {"cookie"}, {"set", "cookie"}, {"api", "key"},
}

// protectedKey: the words of a protected name appear in the key's words as one contiguous
// run; the last word may carry a plural s.
func protectedKey(key string) bool {
	words := keyWords(key)
	for _, name := range protectedNames {
		for i := 0; i+len(name) <= len(words); i++ {
			ok := true
			for j, nw := range name {
				w := words[i+j]
				if w == nw || (j == len(name)-1 && w == nw+"s") {
					continue
				}
				ok = false
				break
			}
			if ok {
				return true
			}
		}
	}
	return false
}

// keyWords splits snake_case, kebab-case, dotted and camelCase keys into lower-case words.
func keyWords(key string) []string {
	var words []string
	for _, part := range strings.FieldsFunc(key, func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
		words = append(words, splitCamel(part)...)
	}
	return words
}

func splitCamel(s string) []string {
	rs := []rune(s)
	var out []string
	start := 0
	for i := 1; i < len(rs); i++ {
		prev, cur := rs[i-1], rs[i]
		next := rune(0)
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		lowerToUpper := !unicode.IsUpper(prev) && unicode.IsUpper(cur)
		acronymEnd := unicode.IsUpper(prev) && unicode.IsUpper(cur) && next != 0 && unicode.IsLower(next)
		if lowerToUpper || acronymEnd {
			out = append(out, strings.ToLower(string(rs[start:i])))
			start = i
		}
	}
	if start < len(rs) {
		out = append(out, strings.ToLower(string(rs[start:])))
	}
	return out
}

// encodeLine writes the envelope first and the other fields sorted; a line above 2 KiB has
// its longest non-envelope strings cut until it fits, and gains truncated: true.
func encodeLine(rec map[string]any) []byte {
	line := marshalOrdered(rec)
	for len(line) > maxLine {
		excess := len(line) - maxLine
		if !cutLongest(rec, excess) {
			break
		}
		rec["truncated"] = true
		line = marshalOrdered(rec)
	}
	return line
}

const truncMark = "…[TRUNCATED]"

// cutLongest shortens the longest string outside the envelope by at least excess bytes.
func cutLongest(rec map[string]any, excess int) bool {
	var best *string
	var set func(string)
	var walk func(v any, assign func(any))
	walk = func(v any, assign func(any)) {
		switch t := v.(type) {
		case string:
			if strings.HasSuffix(t, truncMark) && len(t) <= len(truncMark) {
				return
			}
			if best == nil || len(t) > len(*best) {
				s := t
				best, set = &s, func(n string) { assign(n) }
			}
		case map[string]any:
			for k, x := range t {
				k := k
				walk(x, func(n any) { t[k] = n })
			}
		case []any:
			for i, x := range t {
				i := i
				walk(x, func(n any) { t[i] = n })
			}
		}
	}
	for k, v := range rec {
		if isEnvelope(k) {
			continue
		}
		k := k
		walk(v, func(n any) { rec[k] = n })
	}
	if best == nil {
		return false
	}
	s := strings.TrimSuffix(*best, truncMark)
	keep := len(s) - excess - len(truncMark) - 8 // JSON escaping slack
	if keep < 0 {
		keep = 0
	}
	for keep > 0 && !utf8.RuneStart(s[keep]) {
		keep--
	}
	set(s[:keep] + truncMark)
	return true
}

func marshalOrdered(rec map[string]any) []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	write := func(k string, v any) {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(marshalValue(v))
	}
	for _, k := range envelopeKeys {
		if v, ok := rec[k]; ok {
			write(k, v)
		}
	}
	var rest []string
	for k := range rec {
		if !isEnvelope(k) {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		write(k, rec[k])
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

func marshalValue(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte(`"unencodable"`)
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}
