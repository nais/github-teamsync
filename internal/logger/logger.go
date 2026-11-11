package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// New creates a new logger with the given format and level
func New(format, level string) (*slog.Logger, error) {
	return newLogger(format, level, os.Stderr)
}

func newLogger(format, level string, output io.Writer) (*slog.Logger, error) {
	var parsedLevel slog.Level
	if err := parsedLevel.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", level, err)
	}
	opts := &slog.HandlerOptions{Level: parsedLevel}

	switch strings.ToLower(format) {
	case "json":
		return slog.New(slog.NewJSONHandler(output, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(output, opts)), nil
	default:
		return nil, fmt.Errorf("invalid log format: %q", format)
	}
}
