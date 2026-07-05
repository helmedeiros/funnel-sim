// Package jsonlog emits the platform-standard JSON log shape
// {time, level, msg, attrs}. Matches markup-svc, decision-gateway, and
// traffic-gen so Filebeat's decode_json_fields processor lands every
// service's events in the same platform-logs-* index.
package jsonlog

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

type Logger struct {
	out io.Writer
	mu  sync.Mutex
	now func() time.Time
}

func New(out io.Writer) *Logger {
	return &Logger{out: out, now: time.Now}
}

func (l *Logger) Info(msg string, attrs map[string]any)  { l.write("info", msg, attrs) }
func (l *Logger) Warn(msg string, attrs map[string]any)  { l.write("warn", msg, attrs) }
func (l *Logger) Error(msg string, attrs map[string]any) { l.write("error", msg, attrs) }

func (l *Logger) write(level, msg string, attrs map[string]any) {
	entry := struct {
		Time  string         `json:"time"`
		Level string         `json:"level"`
		Msg   string         `json:"msg"`
		Attrs map[string]any `json:"attrs,omitempty"`
	}{
		Time:  l.now().UTC().Format(time.RFC3339Nano),
		Level: level,
		Msg:   msg,
		Attrs: attrs,
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = json.NewEncoder(l.out).Encode(entry)
}
