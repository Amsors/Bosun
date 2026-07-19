package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestDefaultFieldsArePresentWithoutDuplicatingExplicitValues(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(defaultFieldsHandler{
		Handler:   slog.NewJSONHandler(&output, nil),
		component: "gateway",
	})
	logger.InfoContext(
		context.Background(),
		"request complete",
		"request_id", "request-1",
		"session_id", "session-1",
		"reason", "ok",
	)
	logLine := output.String()
	for _, field := range []string{`"component":"gateway"`, `"request_id":"request-1"`, `"session_id":"session-1"`, `"reason":"ok"`} {
		if !strings.Contains(logLine, field) {
			t.Fatalf("log line missing %s: %s", field, logLine)
		}
	}
	for _, key := range []string{`"request_id"`, `"session_id"`, `"reason"`} {
		if strings.Count(logLine, key) != 1 {
			t.Fatalf("log line contains duplicate %s: %s", key, logLine)
		}
	}
}
