package common

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"
)

func captureLogger(t *testing.T, masks []logMask) *bytes.Buffer {
	t.Helper()

	logMu.Lock()
	oldMasks := logMasks
	oldFullCover := logFullCover
	oldLogger := logLogger
	buf := &bytes.Buffer{}
	logLogger = slog.New(&maskHandler{
		inner: slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
	})
	logMasks = masks
	logFullCover = len(masks) > 0 && masks[0].regex.MatchString("")
	logMu.Unlock()

	t.Cleanup(func() {
		logMu.Lock()
		logMasks = oldMasks
		logFullCover = oldFullCover
		logLogger = oldLogger
		logMu.Unlock()
	})
	return buf
}

func mustMask(t *testing.T, regex string, level slog.Level) logMask {
	t.Helper()
	return logMask{regex: regexp.MustCompile(regex), level: level}
}

func TestMaskFirstMatchWins(t *testing.T) {
	buf := captureLogger(t, []logMask{
		mustMask(t, "^/xcodec", slog.LevelError),
		mustMask(t, "^/", slog.LevelDebug),
	})

	Component("/xcodec/encoder").Debug("hidden")
	Component("/xcodec/encoder").Error("shown-error")
	Component("/wanproxy/proxy").Debug("shown-debug")

	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Errorf("debug message under a stricter first mask was logged:\n%s", out)
	}
	if !strings.Contains(out, "shown-error") {
		t.Errorf("error message was not logged:\n%s", out)
	}
	if !strings.Contains(out, "shown-debug") {
		t.Errorf("debug message under the permissive mask was not logged:\n%s", out)
	}
}

func TestMaskNoMatchLogs(t *testing.T) {
	buf := captureLogger(t, []logMask{
		mustMask(t, "^/xcodec", slog.LevelError),
	})

	Component("/unmatched").Debug("unmatched-debug")

	if !strings.Contains(buf.String(), "unmatched-debug") {
		t.Errorf("message with no matching mask was not logged:\n%s", buf.String())
	}
}

func TestMaskEmptyListLogsEverything(t *testing.T) {
	buf := captureLogger(t, nil)

	Component("/anything").Debug("debug-passes")

	if !strings.Contains(buf.String(), "debug-passes") {
		t.Errorf("with no masks installed every message should log:\n%s", buf.String())
	}
	if Level() != slog.LevelDebug {
		t.Errorf("Level() = %v with no masks, want debug", Level())
	}
}

func TestMaskRecordAttributeOverridesWithState(t *testing.T) {
	buf := captureLogger(t, []logMask{
		mustMask(t, "^/noisy", slog.LevelError),
		mustMask(t, ".?", slog.LevelDebug),
	})

	log := Component("/quiet")
	log.Log(context.Background(), slog.LevelInfo, "message", "component", "/noisy")

	if strings.Contains(buf.String(), "message") {
		t.Errorf("record component /noisy should have suppressed info:\n%s", buf.String())
	}
}

func TestSetLevelReplacesMasks(t *testing.T) {
	captureLogger(t, []logMask{mustMask(t, "^/xcodec", slog.LevelDebug)})

	SetLevel(slog.LevelError)
	if Masks() != 1 {
		t.Fatalf("Masks() = %d after SetLevel, want 1 catch-all", Masks())
	}
	if Level() != slog.LevelError {
		t.Fatalf("Level() = %v, want error", Level())
	}

	ResetMasks()
	if Masks() != 0 {
		t.Fatalf("Masks() = %d after ResetMasks, want 0", Masks())
	}
}

func TestMaskRejectsInvalidRegex(t *testing.T) {
	captureLogger(t, nil)

	if err := Mask("^[unbalanced", slog.LevelInfo); err == nil {
		t.Fatalf("Mask with an invalid regex should fail, like regcomp")
	}
	if Masks() != 0 {
		t.Fatalf("invalid mask was installed")
	}
	if err := Mask("^/xcodec", slog.LevelInfo); err != nil {
		t.Fatalf("Mask with a valid regex failed: %v", err)
	}
	if Masks() != 1 {
		t.Fatalf("valid mask was not installed")
	}
}

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		name string
		want slog.Level
	}{
		{"DEBUG", slog.LevelDebug},
		{"debug", slog.LevelDebug},
		{"INFO", slog.LevelInfo},
		{"WARNING", slog.LevelWarn},
		{"NOTICE", slog.LevelWarn},
		{"ERR", slog.LevelError},
		{"CRIT", slog.LevelError},
		{"ALERT", slog.LevelError},
		{"EMERG", slog.LevelError},
	}
	for _, c := range cases {
		got, err := ParseLogLevel(c.name)
		if err != nil {
			t.Fatalf("ParseLogLevel(%q) failed: %v", c.name, err)
		}
		if got != c.want {
			t.Fatalf("ParseLogLevel(%q) = %v, want %v", c.name, got, c.want)
		}
	}
	if _, err := ParseLogLevel("TRACE"); err == nil {
		t.Fatalf("TRACE is not a C++ config log level and should be rejected")
	}
	if _, err := ParseLogLevel("nonsense"); err == nil {
		t.Fatalf("unknown level should be rejected")
	}
}

func TestEnabledFollowsMostVerboseMask(t *testing.T) {
	captureLogger(t, []logMask{
		mustMask(t, "^/xcodec", slog.LevelDebug),
		mustMask(t, ".?", slog.LevelError),
	})

	h := logLogger.Handler()
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Errorf("Enabled(debug) = false with a debug mask installed")
	}

	captureLogger(t, []logMask{mustMask(t, ".?", slog.LevelError)})
	h = logLogger.Handler()
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Errorf("Enabled(info) = true with only an error catch-all")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Errorf("Enabled(error) = false with an error catch-all")
	}
}
