package common

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
)

const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

type logMask struct {
	regex *regexp.Regexp
	level slog.Level
}

var (
	logMu    sync.RWMutex
	logMasks []logMask

	logFullCover bool

	logLogger = slog.New(&maskHandler{
		inner: slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}),
	})
)

func init() {
	logMasks = []logMask{{regex: regexp.MustCompile(".?"), level: slog.LevelInfo}}
	logFullCover = true
}

func Mask(regex string, level slog.Level) error {
	re, err := regexp.Compile(regex)
	if err != nil {
		return fmt.Errorf("could not compile log mask %q: %w", regex, err)
	}
	logMu.Lock()
	if len(logMasks) == 0 {
		logFullCover = re.MatchString("")
	}
	logMasks = append(logMasks, logMask{regex: re, level: level})
	logMu.Unlock()
	return nil
}

func SetLevel(level slog.Level) {
	logMu.Lock()
	logMasks = []logMask{{regex: regexp.MustCompile(".?"), level: level}}
	logFullCover = true
	logMu.Unlock()
}

func ResetMasks() {
	logMu.Lock()
	logMasks = nil
	logFullCover = false
	logMu.Unlock()
}

func Masks() int {
	logMu.RLock()
	defer logMu.RUnlock()
	return len(logMasks)
}

func Level() slog.Level {
	logMu.RLock()
	defer logMu.RUnlock()
	if len(logMasks) == 0 {
		return slog.LevelDebug
	}
	level := logMasks[0].level
	for _, m := range logMasks[1:] {
		if m.level < level {
			level = m.level
		}
	}
	return level
}

func Logger() *slog.Logger {
	return logLogger
}

func Component(path string) *slog.Logger {
	return Logger().With("component", path)
}

func ParseLogLevel(name string) (slog.Level, error) {
	switch strings.ToUpper(name) {
	case "DEBUG":
		return slog.LevelDebug, nil
	case "INFO":
		return slog.LevelInfo, nil
	case "WARNING", "NOTICE":
		return slog.LevelWarn, nil
	case "ERR", "CRIT", "ALERT", "EMERG":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("unknown log level %q", name)
}

type maskHandler struct {
	inner slog.Handler

	component string
}

func (h *maskHandler) Enabled(_ context.Context, level slog.Level) bool {
	logMu.RLock()
	defer logMu.RUnlock()
	if len(logMasks) == 0 || !logFullCover {
		return true
	}
	for _, m := range logMasks {
		if level >= m.level {
			return true
		}
	}
	return false
}

func (h *maskHandler) Handle(ctx context.Context, r slog.Record) error {
	if !h.allow(r) {
		return nil
	}
	return h.inner.Handle(ctx, r)
}

func (h *maskHandler) allow(r slog.Record) bool {
	component := h.component
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "component" && a.Value.Kind() == slog.KindString {
			component = a.Value.String()
			return false
		}
		return true
	})

	logMu.RLock()
	defer logMu.RUnlock()
	for _, m := range logMasks {
		if m.regex.MatchString(component) {
			return r.Level >= m.level
		}
	}
	return true
}

func (h *maskHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	component := h.component
	for _, a := range attrs {
		if a.Key == "component" && a.Value.Kind() == slog.KindString {
			component = a.Value.String()
		}
	}
	return &maskHandler{inner: h.inner.WithAttrs(attrs), component: component}
}

func (h *maskHandler) WithGroup(name string) slog.Handler {
	return &maskHandler{inner: h.inner.WithGroup(name), component: h.component}
}
