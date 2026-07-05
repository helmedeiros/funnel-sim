package jsonlog

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestLoggerEmitsInfoWithPlatformShape(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	l.Info("test.event", map[string]any{"k": "v", "n": 42})
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; raw=%q", err, buf.String())
	}
	if got["msg"] != "test.event" {
		t.Errorf("msg = %v", got["msg"])
	}
	if got["level"] != "info" {
		t.Errorf("level = %v, want info", got["level"])
	}
	attrs, ok := got["attrs"].(map[string]any)
	if !ok {
		t.Fatalf("attrs missing or wrong shape: %v", got["attrs"])
	}
	if attrs["k"] != "v" {
		t.Errorf("attrs.k = %v", attrs["k"])
	}
	if _, ok := got["time"].(string); !ok {
		t.Errorf("time missing or wrong type: %v", got["time"])
	}
}

func TestLoggerLevelsRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	l.Warn("w", nil)
	l.Error("e", nil)
	dec := json.NewDecoder(strings.NewReader(buf.String()))
	levels := []string{"warn", "error"}
	for _, want := range levels {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if m["level"] != want {
			t.Errorf("level = %v, want %v", m["level"], want)
		}
	}
}
