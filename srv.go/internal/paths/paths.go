// Package paths resolves the repository layout and demo ports.  It is
// the Go port of src/common/paths.ts.  SRV_ROOT is located relative to
// the source tree (which is present both for `go run` and for compiled
// binaries built from this checkout), with environment and
// executable/cwd fallbacks.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// SRV_ROOT is the srv/ directory (this module's root).
var SRV_ROOT = resolveRoot()

// REPO_ROOT is the directory containing srv/, web/ and wanproxy.go/.
var REPO_ROOT = filepath.Dir(SRV_ROOT)

// WANPROXY_GO_ROOT is the wanproxy.go/ checkout next to srv/.
var WANPROXY_GO_ROOT = filepath.Join(REPO_ROOT, "wanproxy.go")

var (
	DataDir      = filepath.Join(SRV_ROOT, "data")
	FilesDir     = filepath.Join(DataDir, "files")
	ToolsDir     = filepath.Join(DataDir, "tools")
	MetaPath     = filepath.Join(DataDir, "files.json")
	ConfigPath   = filepath.Join(DataDir, "wanproxy.conf")
	HostKeyPath  = filepath.Join(DataDir, "ssh-hostkey.pem")
	XCachePath   = filepath.Join(DataDir, "wanproxy.xcache")
	TemplatePath = filepath.Join(SRV_ROOT, "demo", "wanproxy.conf.template")

	WanproxyBin = filepath.Join(WANPROXY_GO_ROOT, "bin", "wanproxy")
	TackBin     = filepath.Join(WANPROXY_GO_ROOT, "bin", "tack")
	XcdumpBin   = filepath.Join(WANPROXY_GO_ROOT, "bin", "xcdump")
)

func resolveRoot() string {
	if env := os.Getenv("SRV_ROOT"); env != "" {
		if abs, err := filepath.Abs(env); err == nil {
			return abs
		}
		return env
	}
	// Source-relative: this file lives at <srv>/internal/paths/paths.go.
	if _, file, _, ok := runtime.Caller(0); ok {
		cand := filepath.Dir(filepath.Dir(filepath.Dir(file)))
		if looksLikeRoot(cand) {
			return cand
		}
	}
	// Walk up from the executable, then from the working directory.
	if exe, err := os.Executable(); err == nil {
		if cand := walkUp(filepath.Dir(exe)); cand != "" {
			return cand
		}
	}
	if wd, err := os.Getwd(); err == nil {
		if cand := walkUp(wd); cand != "" {
			return cand
		}
	}
	wd, _ := os.Getwd()
	return wd
}

func walkUp(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if looksLikeRoot(d) {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
	}
}

func looksLikeRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "demo", "wanproxy.conf.template"))
	return err == nil
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// SRVPort is the HTTP listen port (and the origin the proxies forward
// to).  PORT takes precedence over SRV_PORT, matching main.ts.
func SRVPort() int {
	if v := os.Getenv("PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return envInt("SRV_PORT", 3101)
}

// MonitorPort is the wanproxy monitor port.
func MonitorPort() int { return envInt("MONITOR_PORT", 9901) }

// SSHEchoPort is the in-process SSH echo endpoint port.
func SSHEchoPort() int { return envInt("SSH_ECHO_PORT", 8349) }

// EnsureDirs creates the runtime data directories.
func EnsureDirs() {
	for _, dir := range []string{DataDir, FilesDir, ToolsDir} {
		_ = os.MkdirAll(dir, 0o755)
	}
}
