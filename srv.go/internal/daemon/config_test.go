package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hangtiancheng/wanproxy/srv.go/internal/paths"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/config"
)

func TestRenderJSONConfig(t *testing.T) {
	dir := t.TempDir()
	oldConfig, oldCache, oldKey := paths.ConfigPath, paths.XCachePath, paths.HostKeyPath
	defer func() { paths.ConfigPath, paths.XCachePath, paths.HostKeyPath = oldConfig, oldCache, oldKey }()
	paths.ConfigPath = filepath.Join(dir, "wanproxy.json")
	paths.XCachePath = filepath.Join(dir, "cache with a quote \" and slash \\")
	paths.HostKeyPath = filepath.Join(dir, "host key.pem")
	t.Setenv("SRV_PORT", "3123")
	t.Setenv("PORT", "")
	t.Setenv("MONITOR_PORT", "9912")
	t.Setenv("SSH_ECHO_PORT", "8359")
	if err := ensureHostKey(); err != nil {
		t.Fatal(err)
	}
	if err := renderConfig(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	caches := file["caches"].(map[string]any)
	disk := caches["diskcache0"].(map[string]any)
	if disk["path"] != paths.XCachePath {
		t.Fatal("path was not JSON escaped")
	}
	// Keep the test volume small; parsing also verifies the production template
	// against the same loader the child daemon uses.
	disk["size"] = "4MB"
	disk["path"] = filepath.Join(dir, "cache")
	data, err = json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	if len(cfg.Proxies) != 10 || cfg.Monitor == nil {
		t.Fatalf("demo topology: %+v", cfg)
	}
	if cfg.Proxies[0].Upstream.Port != "3123" || cfg.Monitor.Port != "9912" || cfg.Proxies[9].Upstream.Port != "8359" {
		t.Fatal("runtime port overrides were not applied")
	}
}
