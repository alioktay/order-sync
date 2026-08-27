package shared

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	buffer := &bytes.Buffer{}
	log.SetOutput(buffer)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(writer)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
	})
	return buffer
}

func TestJSONLoggerWritesStructuredEntries(t *testing.T) {
	buffer := captureLogs(t)
	logger := NewLogger()
	logger.InfoContext(context.Background(), "worker started", "jobs", 3)

	var entry map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buffer.String())), &entry); err != nil {
		t.Fatalf("invalid log JSON: %v", err)
	}
	if entry["level"] != "info" || entry["message"] != "worker started" || entry["jobs"] != float64(3) {
		t.Fatalf("unexpected log entry: %#v", entry)
	}
	if _, err := time.Parse(time.RFC3339Nano, entry["timestamp"].(string)); err != nil {
		t.Fatalf("invalid timestamp: %v", err)
	}

	buffer.Reset()
	logger.ErrorContext(context.Background(), "worker failed", "attempt", 2)
	if err := json.Unmarshal([]byte(strings.TrimSpace(buffer.String())), &entry); err != nil || entry["level"] != "error" {
		t.Fatalf("unexpected error log: %#v, %v", entry, err)
	}
}

func TestJSONLoggerHandlesUnsupportedContext(t *testing.T) {
	buffer := captureLogs(t)
	NewLogger().InfoContext(context.Background(), "cannot encode", slog.Any("channel", make(chan int)))
	line := strings.TrimSpace(buffer.String())
	if !strings.Contains(line, `"message":"cannot encode"`) || !strings.Contains(line, `"channel"`) {
		t.Fatalf("unexpected fallback log: %s", line)
	}
}
