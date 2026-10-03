package fakes

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// Logs is the log-capture half of fake/observer: every line the component writes, by stream.
type Logs struct {
	mu    sync.Mutex
	lines []LogLine
}

// LogLine is one captured line; JSON is nil when the line is not one JSON object.
type LogLine struct {
	Stream string // stdout or stderr
	Raw    string
	JSON   map[string]any
	At     time.Time
}

// NewLogs makes an empty capture.
func NewLogs() *Logs { return &Logs{} }

// Feed records one line.
func (l *Logs) Feed(stream, line string) {
	line = strings.TrimRight(line, "\r\n")
	e := LogLine{Stream: stream, Raw: line, At: time.Now()}
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) == nil {
		e.JSON = m
	}
	l.mu.Lock()
	l.lines = append(l.lines, e)
	l.mu.Unlock()
}

// Lines returns every line.
func (l *Logs) Lines() []LogLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LogLine(nil), l.lines...)
}

// Find returns the lines matching pred.
func (l *Logs) Find(pred func(LogLine) bool) []LogLine {
	var out []LogLine
	for _, e := range l.Lines() {
		if pred(e) {
			out = append(out, e)
		}
	}
	return out
}

// Str returns a string field of a JSON line ("" when absent or not a string).
func (e LogLine) Str(k string) string {
	s, _ := e.JSON[k].(string)
	return s
}
