package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

var SRV_ROOT = resolveRoot()

var REPO_ROOT = filepath.Dir(SRV_ROOT)

var WANPROXY_GO_ROOT = filepath.Join(REPO_ROOT, "wanproxy.go")

var (
	DataDir           = filepath.Join(SRV_ROOT, "data")
	FilesDir          = filepath.Join(DataDir, "files")
	ToolsDir          = filepath.Join(DataDir, "tools")
	MetaPath          = filepath.Join(DataDir, "files.json")
	ConfigPath        = filepath.Join(DataDir, "wanproxy.json")
	HostKeyPath       = filepath.Join(DataDir, "ssh-hostkey.pem")
	XCachePath        = filepath.Join(DataDir, "wanproxy.xcache")
	ExampleConfigPath = filepath.Join(SRV_ROOT, "demo", "wanproxy.example.json")

	WanproxyBin = binPath("wanproxy")
	TackBin     = binPath("tack")
	XcdumpBin   = binPath("xcdump")
)

func binPath(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(WANPROXY_GO_ROOT, "bin", name)
}

func resolveRoot() string {
	if env := os.Getenv("SRV_ROOT"); env != "" {
		if abs, err := filepath.Abs(env); err == nil {
			return abs
		}
		return env
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		cand := filepath.Dir(filepath.Dir(filepath.Dir(file)))
		if looksLikeRoot(cand) {
			return cand
		}
	}
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
	_, err := os.Stat(filepath.Join(dir, "demo", "wanproxy.example.json"))
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

func SRVPort() int {
	if v := os.Getenv("PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return envInt("SRV_PORT", 3101)
}

func MonitorPort() int { return envInt("MONITOR_PORT", 9901) }

func SSHEchoPort() int { return envInt("SSH_ECHO_PORT", 8349) }

func EnsureDirs() {
	for _, dir := range []string{DataDir, FilesDir, ToolsDir} {
		_ = os.MkdirAll(dir, 0o755)
	}
}
