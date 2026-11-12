package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// New creates a new logger with the given format and level
func New(format string, level slog.Level) (*slog.Logger, error) {
	return newLogger(format, level, os.Stderr)
}

func newLogger(format string, level slog.Level, output io.Writer) (*slog.Logger, error) {
	opts := &slog.HandlerOptions{Level: level}

	switch strings.ToLower(format) {
	case "json":
		return slog.New(slog.NewJSONHandler(output, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(output, opts)), nil
	default:
		return nil, fmt.Errorf("invalid log format: %q", format)
	}
}
