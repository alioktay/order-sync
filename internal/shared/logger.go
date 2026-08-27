package shared

import (
	"log"
	"log/slog"
	"strings"
)

// NewLogger returns the process logger with a JSON handler. The handler is
// created from log.Writer so callers can redirect standard logging in tests.
func NewLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(log.Writer(), &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if len(groups) != 0 {
				return attr
			}
			switch attr.Key {
			case slog.TimeKey:
				attr.Key = "timestamp"
			case slog.LevelKey:
				attr.Value = slog.StringValue(strings.ToLower(attr.Value.String()))
			case slog.MessageKey:
				attr.Key = "message"
			}
			return attr
		},
	}))
}
