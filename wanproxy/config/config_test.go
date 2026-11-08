package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hangtiancheng/wanproxy/wanproxy/common"
	"github.com/hangtiancheng/wanproxy/wanproxy/pipeline"
	"github.com/hangtiancheng/wanproxy/wanproxy/proxy"
)

const minimalConfig = `{"proxies":[{"name":"socks","type":"socks","listen":{"host":"127.0.0.1","port":0}}]}`

func writeTestHostKey(t *testing.T, dir string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	path := filepath.Join(dir, "host key.pem")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCompleteJSONConfig(t *testing.T) {
	dir := t.TempDir()
	key := writeTestHostKey(t, dir)
	disk := filepath.Join(dir, "cache volume")
	input := fmt.Sprintf(`{
 "$schema":"schema.json",
 "log":{"level":"debug","masks":[{"regex":"^/xcodec","level":"error"}]},
 "caches":{
  "a-pair":{"type":"pair","primary":"memory","secondary":"disk"},
  "disk":{"type":"disk","size":"4MB","path":%q},
  "memory":{"type":"memory","size":"128MB"}
 },
 "codecs":{
  "encode":{"mode":"zlib+xcodec","cache":"a-pair","compressorLevel":6,"trackStatistics":true},
  "decode":{"mode":"xcodec","cache":"a-pair"}
 },
 "proxies":[
  {"name":"tcp","type":"tcp","listen":{"host":"127.0.0.1","port":0},"upstream":{"family":"ipv6","host":"::1","port":3301},"peerCodec":"encode","interfaceCodec":"decode"},
  {"name":"udp","type":"udp","listen":{"family":"ipv4","host":"127.0.0.1","port":0},"upstream":{"host":"localhost","port":53},"idleTimeoutSeconds":10,"maxSessions":12},
  {"name":"ssh","type":"ssh","listen":{"host":"127.0.0.1","port":0},"upstream":{"host":"localhost","port":3301},"hostKey":%q,"handshakeTimeoutSeconds":3},
  {"name":"socks","type":"socks","listen":{"family":"unix","path":"/tmp/wanproxy-test.sock"}}
 ],"monitor":{"listen":{"host":"127.0.0.1","port":0}}
 }`, disk, key)
	before := common.Masks()
	cfg, err := Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	if common.Masks() != before {
		t.Fatal("parsing mutated global logging")
	}
	if cfg.LogLevel != slog.LevelDebug || len(cfg.LogMasks) != 1 || cfg.LogMasks[0].Level != slog.LevelError {
		t.Fatalf("logging: %+v", cfg)
	}
	if len(cfg.Proxies) != 4 || cfg.Monitor == nil {
		t.Fatalf("shape: %+v", cfg)
	}
	p := cfg.Proxies[0]
	if p.PeerCodec.Mode != pipeline.ModeZlibXCodec || p.PeerCodec.CompressorLevel != 6 || p.PeerCodec.Stats == nil || p.InterfaceCodec.Cache != p.PeerCodec.Cache {
		t.Fatalf("codecs: %+v", p)
	}
	if p.Upstream.Network() != "tcp6" || p.Listen.Port != "0" {
		t.Fatalf("endpoints: %+v", p)
	}
	if cfg.Proxies[1].MaxSessions != 12 || cfg.Proxies[1].IdleTimeout != 10*time.Second {
		t.Fatal("lost UDP options")
	}
	if cfg.Proxies[2].HostKey == nil || cfg.Proxies[2].HandshakeTimeout != 3*time.Second {
		t.Fatal("lost SSH options")
	}
	if cfg.Proxies[3].Listen.Family != proxy.FamilyUnix {
		t.Fatal("lost Unix endpoint")
	}
	st, err := os.Stat(disk)
	if err != nil || st.Size() != 4<<20 {
		t.Fatalf("disk: %v %v", st, err)
	}
	if len(cfg.Objects) != 9 {
		t.Fatalf("monitor objects: %d", len(cfg.Objects))
	}
}

func TestJSONErrors(t *testing.T) {
	for name, input := range map[string]string{
		"non-JSON":          "create proxy p\nactivate p",
		"unknown top-level": `{"socksProxies":[{"name":"s","listen":{"host":"localhost","port":0}}]}`,
		"empty":             `{}`,
		"null":              "null",
		"array":             "[]",
		"unknown":           strings.Replace(minimalConfig, `"type":"socks"`, `"type":"socks","bogus":true`, 1),
		"missing port":      strings.Replace(minimalConfig, `,"port":0`, "", 1),
		"null port":         strings.Replace(minimalConfig, `"port":0`, `"port":null`, 1),
		"duplicate key":     strings.Replace(minimalConfig, `"type":"socks"`, `"type":"tcp","type":"socks"`, 1),
		"trailing":          minimalConfig + `{}`,
		"bad log level":     strings.Replace(minimalConfig, `{"proxies"`, `{"log":{"level":"DEBUG"},"proxies"`, 1),
		"bad regex":         strings.Replace(minimalConfig, `{"proxies"`, `{"log":{"masks":[{"regex":"[","level":"info"}]},"proxies"`, 1),
		"missing regex":     strings.Replace(minimalConfig, `{"proxies"`, `{"log":{"masks":[{"level":"info"}]},"proxies"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if cfg, err := Parse([]byte(input)); err == nil {
				cfg.Close()
				t.Fatal("accepted invalid config")
			}
		})
	}
}

func TestProxyValidation(t *testing.T) {
	base := map[string]any{"name": "proxy", "type": "udp", "listen": map[string]any{"host": "127.0.0.1", "port": 0}, "upstream": map[string]any{"host": "localhost", "port": 53}}
	for name, mutate := range map[string]func(map[string]any){
		"missing type":      func(p map[string]any) { delete(p, "type") },
		"unknown type":      func(p map[string]any) { p["type"] = "UDP-UDP" },
		"upstream zero":     func(p map[string]any) { p["upstream"].(map[string]any)["port"] = 0 },
		"bad family":        func(p map[string]any) { p["listen"].(map[string]any)["family"] = "" },
		"UDP codec":         func(p map[string]any) { p["peerCodec"] = "none" },
		"UDP Unix":          func(p map[string]any) { p["listen"] = map[string]any{"family": "unix", "path": "/tmp/x"} },
		"UDP SSH key":       func(p map[string]any) { p["hostKey"] = "file" },
		"zero idle":         func(p map[string]any) { p["idleTimeoutSeconds"] = 0 },
		"huge idle":         func(p map[string]any) { p["idleTimeoutSeconds"] = 86401 },
		"negative sessions": func(p map[string]any) { p["maxSessions"] = -1 },
		"huge sessions":     func(p map[string]any) { p["maxSessions"] = 65537 },
		"wrong timeout":     func(p map[string]any) { p["handshakeTimeoutSeconds"] = 1 },
		"TCP UDP settings":  func(p map[string]any) { p["type"] = "tcp"; p["maxSessions"] = 1 },
		"missing SSH key":   func(p map[string]any) { p["type"] = "ssh" },
		"SOCKS upstream":    func(p map[string]any) { p["type"] = "socks" },
		"unknown codec":     func(p map[string]any) { p["type"] = "tcp"; p["interfaceCodec"] = "missing" },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var p map[string]any
			json.Unmarshal(raw, &p)
			mutate(p)
			data, _ := json.Marshal(map[string]any{"proxies": []any{p}})
			if cfg, err := Parse(data); err == nil {
				cfg.Close()
				t.Fatal("accepted invalid proxy")
			}
		})
	}
}

func TestCacheAndCodecValidation(t *testing.T) {
	for _, fields := range []string{
		`"caches":{"a":{"type":"pair","primary":"a","secondary":"a"}}`,
		`"caches":{"a":{"type":"pair","primary":"b","secondary":"b"},"b":{"type":"pair","primary":"a","secondary":"a"}}`,
		`"caches":{"a":{"type":"pair","primary":"missing","secondary":"missing"}}`,
		`"caches":{"a":{"type":"memory","path":"bad"}}`,
		`"caches":{"a":{"type":"disk"}}`,
		`"caches":{"a":{"type":"memory","size":"-1MB"}}`,
		`"codecs":{"c":{"mode":"xcodec"}}`,
		`"codecs":{"c":{"mode":"XCodec"}}`,
		`"codecs":{"c":{"mode":"zlib","compressorLevel":10}}`,
		`"codecs":{"c":{"mode":"none","compressorLevel":0}}`,
		`"codecs":{"c":{"mode":"none","cache":"missing"}}`,
	} {
		input := strings.Replace(minimalConfig, `{`, `{`+fields+`,`, 1)
		if cfg, err := Parse([]byte(input)); err == nil {
			cfg.Close()
			t.Fatalf("accepted %s", fields)
		}
	}
}

func TestLoadJSONOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.json")
	if err := os.WriteFile(path, []byte(minimalConfig), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Close()
	if err := os.WriteFile(path, []byte("create proxy x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("accepted non-JSON configuration")
	}
}

func TestParseSize(t *testing.T) {
	for input, want := range map[string]int64{"0": 0, "512": 512, "1kb": 1024, "128MB": 128 << 20, "1GB": 1 << 30, "2tb": 2 << 40} {
		if got, err := ParseSize(input); err != nil || got != want {
			t.Fatalf("ParseSize(%q)=%d,%v", input, got, err)
		}
	}
	for _, input := range []string{"", "-1", "-1MB", "0x10", "1.5MB", "12qb", "9223372036854775807KB", "8388608TB"} {
		if _, err := ParseSize(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
