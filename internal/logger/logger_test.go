package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestNew(t *testing.T) {
	log, err := New("json", "info")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := log.Handler().(*slog.JSONHandler); !ok {
		t.Errorf("handler = %T, want *slog.JSONHandler", log.Handler())
	}
}

func TestJSONOutput(t *testing.T) {
	var output bytes.Buffer
	log, err := newLogger("JSON", "debug", &output)
	if err != nil {
		t.Fatal(err)
	}
	log.With("organization", "example").WarnContext(t.Context(), "sync failed", "error", errors.New("unavailable"), "teams", 2)
	var got map[string]any
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	stamp, ok := got["time"].(string)
	if !ok {
		t.Fatalf("missing timestamp: %v", got)
	}
	if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
		t.Fatalf("invalid timestamp: %v", err)
	}
	delete(got, "time")
	want := map[string]any{
		"level": "WARN", "msg": "sync failed", "organization": "example", "error": "unavailable", "teams": float64(2),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("log mismatch (-want +got):\n%s", diff)
	}
}

func TestTextOutput(t *testing.T) {
	var output bytes.Buffer
	log, err := newLogger("TEXT", "info", &output)
	if err != nil {
		t.Fatal(err)
	}
	log.InfoContext(t.Context(), "sync completed", "organization", "example")
	for _, want := range []string{"time=", "level=INFO", `msg="sync completed"`, "organization=example"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("log %q does not contain %q", output.String(), want)
		}
	}
}

func TestLevels(t *testing.T) {
	for _, tc := range []struct {
		name  string
		level slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"DEBUG+2", slog.LevelDebug + 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			log, err := newLogger("json", tc.name, &output)
			if err != nil {
				t.Fatal(err)
			}
			log.Log(t.Context(), tc.level-1, "filtered")
			if output.Len() != 0 {
				t.Errorf("below-threshold log was not filtered: %s", &output)
			}
			log.Log(t.Context(), tc.level, "visible")
			if output.Len() == 0 {
				t.Error("threshold-level log was filtered")
			}
		})
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		format, level, want string
	}{
		{"invalid", "info", "invalid log format"},
		{"", "info", "invalid log format"},
		{"json", "invalid", "invalid log level"},
		{"json", "", "invalid log level"},
	} {
		t.Run(tc.format+"/"+tc.level, func(t *testing.T) {
			log, err := newLogger(tc.format, tc.level, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
			if log != nil {
				t.Error("invalid configuration must not return a logger")
			}
		})
	}
}
