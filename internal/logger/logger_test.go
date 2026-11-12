package logger

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	for _, format := range []string{"json", "JSON", "text", "TEXT"} {
		t.Run(format, func(t *testing.T) {
			log, err := New(format, slog.LevelInfo)
			if err != nil {
				t.Fatal(err)
			}
			switch log.Handler().(type) {
			case *slog.JSONHandler:
				if strings.ToLower(format) != "json" {
					t.Errorf("handler = %T, want text handler", log.Handler())
				}
			case *slog.TextHandler:
				if strings.ToLower(format) != "text" {
					t.Errorf("handler = %T, want JSON handler", log.Handler())
				}
			default:
				t.Errorf("unexpected handler: %T", log.Handler())
			}
		})
	}
}

func TestLevel(t *testing.T) {
	var output bytes.Buffer
	const level = slog.LevelDebug + 2
	log, err := newLogger("json", level, &output)
	if err != nil {
		t.Fatal(err)
	}
	log.Log(t.Context(), level-1, "filtered")
	if output.Len() != 0 {
		t.Errorf("below-threshold log was not filtered: %s", &output)
	}
	log.Log(t.Context(), level, "visible")
	if output.Len() == 0 {
		t.Error("threshold-level log was filtered")
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		format, want string
	}{
		{"invalid", "invalid log format"},
		{"", "invalid log format"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			log, err := newLogger(tc.format, slog.LevelInfo, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
			if log != nil {
				t.Error("invalid configuration must not return a logger")
			}
		})
	}
}
