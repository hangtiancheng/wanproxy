// Package common provides small shared helpers, starting with the
// logging setup that mirrors the C++ Log system's level masks.
//
// Like the C++ implementation (common/log.cc), masks are an ordered
// list of (component regex, level) pairs installed with Mask.  When a
// message is logged, the first mask whose regex matches the message's
// component decides the outcome: the message is logged when its level
// is at least as severe as the mask's level, and suppressed otherwise.
// A message whose component matches no mask is logged, and with no
// masks installed at all every message is logged, exactly as in the
// C++ implementation.
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

// Levels supported by the C++ log-mask configuration, mapped onto slog.
const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// logMask mirrors the C++ LogMask: a compiled component regex and the
// most verbose level that matching components may log.
type logMask struct {
	regex *regexp.Regexp
	level slog.Level
}

var (
	logMu    sync.RWMutex
	logMasks []logMask

	// logFullCover reports whether the first installed mask matches
	// every possible component, so that no component can fall through
	// to the C++ "no mask matches, log it" rule.  Go regular
	// expressions match unanchored, so a regex that matches the empty
	// string matches every string.
	logFullCover bool

	// The Go default mirrors what the C++ programs install from main
	// before parsing configuration: a catch-all mask at Info.
	logLogger = slog.New(&maskHandler{
		inner: slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}),
	})
)

func init() {
	logMasks = []logMask{{regex: regexp.MustCompile(".?"), level: slog.LevelInfo}}
	logFullCover = true
}

// Mask installs a component mask, like the C++ Log::mask(handle_regex,
// priority): messages whose component matches the regex are logged when
// their level is at least as severe as level.  Masks are consulted in
// the order they were installed and the first match wins.  An error is
// returned when the regex does not compile, like regcomp failing in the
// C++ implementation.
func Mask(regex string, level slog.Level) error {
	re, err := regexp.Compile(regex)
	if err != nil {
		return fmt.Errorf("could not compile log mask %q: %w", regex, err)
	}
	logMu.Lock()
	if len(logMasks) == 0 {
		// The first mask decides every component it matches; when it
		// cannot match them all, unmatched components log.
		logFullCover = re.MatchString("")
	}
	logMasks = append(logMasks, logMask{regex: re, level: level})
	logMu.Unlock()
	return nil
}

// SetLevel sets the global log level, like the Log::mask(".?", level)
// call the C++ programs make from main: it replaces every previously
// installed mask with a single catch-all mask.
func SetLevel(level slog.Level) {
	logMu.Lock()
	logMasks = []logMask{{regex: regexp.MustCompile(".?"), level: level}}
	logFullCover = true
	logMu.Unlock()
}

// ResetMasks removes every installed mask.  With no masks installed
// every message is logged, matching the C++ behaviour of an empty
// log_masks list.
func ResetMasks() {
	logMu.Lock()
	logMasks = nil
	logFullCover = false
	logMu.Unlock()
}

// Masks returns the number of installed masks.
func Masks() int {
	logMu.RLock()
	defer logMu.RUnlock()
	return len(logMasks)
}

// Level returns the most verbose level any installed mask allows.  With
// no masks installed every level passes, so LevelDebug is returned.
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

// Logger returns the shared logger.
func Logger() *slog.Logger {
	return logLogger
}

// Component returns a logger tagged with a C++ style component path,
// e.g. "/wanproxy/proxy/proxy0".  The component is what log masks are
// matched against, like the C++ LogHandle.
func Component(path string) *slog.Logger {
	return Logger().With("component", path)
}

// ParseLogLevel maps the C++ log level names of config_type_log_level
// onto slog levels, preserving which messages each mask admits: a C++
// mask admits a message when its priority is at least as severe as the
// mask, and slog admits a record when its level is at least as severe
// as the handler level, so EMERG through ERR map onto LevelError (they
// all suppress WARNING and below), WARNING and NOTICE map onto
// LevelWarn (both suppress INFO), INFO maps onto LevelInfo and DEBUG
// maps onto LevelDebug.  Unknown names are rejected, like the C++
// ConfigTypeEnum set which fails on values outside the mapping.
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

// maskHandler applies the C++ Log::mask rules to slog records.  The
// inner handler does no level filtering of its own; every decision is
// made from the mask list and the record's component.
type maskHandler struct {
	inner slog.Handler

	// component is the component captured from WithAttrs, used when the
	// record itself carries no component attribute.
	component string
}

// Enabled reports whether any installed mask could allow a record at
// the given level.  With no masks every record passes, like the C++
// fall-through.  When the first mask does not cover every possible
// component, a component matching no mask logs at any level, so every
// level is admitted and the per-component decision is made in Handle.
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

// Handle emits the record through the inner handler when the mask rules
// allow it.
func (h *maskHandler) Handle(ctx context.Context, r slog.Record) error {
	if !h.allow(r) {
		return nil
	}
	return h.inner.Handle(ctx, r)
}

// allow applies the C++ mask walk: the first mask whose regex matches
// the record's component decides whether it is logged; when no mask
// matches the record is logged.
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
