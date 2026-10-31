package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/common"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/proxy"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

// releaseDisk closes the process-wide disk cache volume at path after the
// test. Windows cannot remove a directory while files in it are open.
func releaseDisk(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() {
		if disk, err := xcodec.OpenDisk(path, 0, slog.Default()); err == nil {
			_ = disk.Close()
		}
	})
}

// The exact wanproxy.conf shipped with the C++ implementation.
const cppSampleConfig = `# Set log-mask before doing anything else so that there isn't a lot of noise.
# Uses an informative mask in non-release builds.  Errors-only in release builds.
create log-mask catch-all
set catch-all.regex "^/"
set catch-all.mask INFO
activate catch-all

# Set up cache hierarchy:
# A primary in-memory cache of 128MB per peer.
# A secondary disk cache of 1GB in the file wanproxy.xcache shared by all peers.
create cache memorycache0
set memorycache0.type Memory
set memorycache0.size 128MB
activate memorycache0

create cache diskcache0
set diskcache0.type Disk
set diskcache0.size 1GB
set diskcache0.path "` + "wanproxy-test.xcache" + `"
activate diskcache0

create cache cache0
set cache0.type Pair
set cache0.primary memorycache0
set cache0.secondary diskcache0
activate cache0

# Set up codec instances.
create codec codec0
set codec0.codec XCodec
set codec0.cache cache0
set codec0.compressor zlib
set codec0.compressor_level 6
set codec0.track_statistics true
activate codec0

create codec codec1
set codec1.codec XCodec
set codec1.cache cache0
set codec1.compressor zlib
set codec1.compressor_level 6
set codec1.track_statistics true
activate codec1

# Set up interfaces.
create interface if0
set if0.family IP
set if0.host "localhost"
set if0.port "3300"
activate if0

create interface if1
set if1.family $if0.family
set if1.host $if0.host
set if1.port "3301"
activate if1

create interface if2
set if2.family $if0.family
set if2.host $if0.host
set if2.port "3302"
activate if2

# And peers to connect to them.
create peer peer0
set peer0.family $if1.family
set peer0.host $if1.host
set peer0.port $if1.port
activate peer0

create peer peer1
set peer1.family $if2.family
set peer1.host $if2.host
set peer1.port $if2.port
activate peer1

# Set up proxy from the client that encodes.
create proxy proxy0
set proxy0.type TCP-TCP
set proxy0.interface if0
set proxy0.interface_codec None
set proxy0.peer peer0
set proxy0.peer_codec codec0
activate proxy0

# Which feeds into this, which decodes.
create proxy proxy1
set proxy1.type TCP-TCP
set proxy1.interface if1
set proxy1.interface_codec codec1
set proxy1.peer peer1
set proxy1.peer_codec None
activate proxy1

# Which feeds into this, which spawns connections from SOCKS.
create proxy-socks proxy-socks0
set proxy-socks0.interface if2
activate proxy-socks0

# And set up a monitoring interface on port 9900.
create interface if3
set if3.family $if0.family
set if3.host $if0.host
set if3.port "9900"
activate if3

create monitor monitor0
set monitor0.interface if3
activate monitor0
`

func TestParseCppSampleConfig(t *testing.T) {
	dir := t.TempDir()
	text := strings.Replace(cppSampleConfig, "wanproxy-test.xcache", dir+"/wanproxy.xcache", 1)

	cfg, err := NewClassicParser().Parse(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	releaseDisk(t, dir+"/wanproxy.xcache")

	if cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("log level = %v, want info", cfg.LogLevel)
	}
	if len(cfg.Proxies) != 2 {
		t.Fatalf("proxies = %d, want 2", len(cfg.Proxies))
	}
	if len(cfg.SocksProxies) != 1 {
		t.Fatalf("socks proxies = %d, want 1", len(cfg.SocksProxies))
	}
	if cfg.Monitor == nil || cfg.Monitor.Interface.Port != "9900" {
		t.Fatalf("monitor = %+v", cfg.Monitor)
	}

	// proxy0: client side; codec on the peer slot.
	p0 := cfg.Proxies[0]
	if p0.Name != "proxy0" {
		t.Fatalf("proxy0 name = %q", p0.Name)
	}
	if p0.Interface.Host != "localhost" || p0.Interface.Port != "3300" {
		t.Fatalf("proxy0 interface = %+v", p0.Interface)
	}
	if p0.InterfaceCodec != nil {
		t.Fatalf("proxy0 interface_codec should be None")
	}
	if p0.Peer.Port != "3301" {
		t.Fatalf("proxy0 peer = %+v", p0.Peer)
	}
	if p0.PeerCodec == nil {
		t.Fatalf("proxy0 peer_codec missing")
	}
	if p0.PeerCodec.Mode != pipeline.ModeZlibXCodec {
		t.Fatalf("proxy0 peer codec mode = %v", p0.PeerCodec.Mode)
	}
	if p0.PeerCodec.CompressorLevel != 6 {
		t.Fatalf("proxy0 peer codec level = %d", p0.PeerCodec.CompressorLevel)
	}
	if !p0.PeerCodec.TrackStatistics || p0.PeerCodec.Stats == nil {
		t.Fatalf("proxy0 peer codec stats missing")
	}

	// proxy1: server side; codec on the interface slot.
	p1 := cfg.Proxies[1]
	if p1.InterfaceCodec == nil || p1.PeerCodec != nil {
		t.Fatalf("proxy1 codec slots wrong: iface=%v peer=%v", p1.InterfaceCodec, p1.PeerCodec)
	}
	if p1.Interface.Port != "3301" || p1.Peer.Port != "3302" {
		t.Fatalf("proxy1 endpoints = %+v / %+v", p1.Interface, p1.Peer)
	}

	// Both codecs share the same activated pair cache.
	if p0.PeerCodec.Cache != p1.InterfaceCodec.Cache {
		t.Fatalf("codecs should share cache0")
	}

	if cfg.SocksProxies[0].Interface.Port != "3302" {
		t.Fatalf("socks interface = %+v", cfg.SocksProxies[0].Interface)
	}

	// Objects are retained for monitor export.
	if len(cfg.Objects) < 15 {
		t.Fatalf("objects = %d, want all activated objects", len(cfg.Objects))
	}
}

func TestClassicParserErrors(t *testing.T) {
	cases := map[string]string{
		"unknown command":   "frobnicate foo\ncreate interface if0\nactivate if0",
		"unknown class":     "create frob x\n",
		"duplicate create":  "create interface if0\ncreate interface if0\n",
		"bad set target":    "set missing.host \"x\"\n",
		"bad set ident":     "create interface if0\nset if0 \"x\"\n",
		"bad ref":           "create interface if0\nset if0.host $nope.host\n",
		"unset ref":         "create interface if0\ncreate interface if1\nset if1.host $if0.host\n",
		"activate missing":  "activate nope\n",
		"proxy needs iface": "create proxy p0\nactivate p0\n",
		"ssh proxy": `create interface if0
set if0.family IP
set if0.host "localhost"
set if0.port "1"
activate if0
create peer peer0
set peer0.family IP
set peer0.host "localhost"
set peer0.port "2"
activate peer0
create proxy p0
set p0.type SSH-SSH
set p0.interface if0
set p0.peer peer0
activate p0
`,
		"unix with host": `create interface if0
set if0.family Unix
set if0.host "localhost"
set if0.path "/tmp/x"
activate if0
create proxy-socks s0
set s0.interface if0
activate s0
`,
	}
	for name, text := range cases {
		if _, err := NewClassicParser().Parse(text); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestClassicLogMaskActivation(t *testing.T) {
	common.ResetMasks()
	t.Cleanup(common.ResetMasks)
	before := common.Masks()

	const text = `
create log-mask xcodec-errors
set xcodec-errors.regex "^/xcodec"
set xcodec-errors.mask ERR
activate xcodec-errors

create log-mask catch-all
set catch-all.regex "^/"
set catch-all.mask DEBUG
activate catch-all
`
	cfg, err := NewClassicParser().Parse(text + minimalSocksProxy)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(cfg.LogMasks) != 2 {
		t.Fatalf("log masks = %d, want 2", len(cfg.LogMasks))
	}
	if cfg.LogMasks[0].Name != "xcodec-errors" || cfg.LogMasks[0].Regex != "^/xcodec" || cfg.LogMasks[0].Level != slog.LevelError {
		t.Fatalf("first mask = %+v", cfg.LogMasks[0])
	}
	if cfg.LogMasks[1].Name != "catch-all" || cfg.LogMasks[1].Regex != "^/" || cfg.LogMasks[1].Level != slog.LevelDebug {
		t.Fatalf("second mask = %+v", cfg.LogMasks[1])
	}
	// The single-level view keeps the most detailed mask.
	if cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("log level = %v, want debug", cfg.LogLevel)
	}
	// Activation installs the masks on the shared logger, like the C++
	// ConfigClassLogMask activation calling Log::mask.
	if common.Masks() != before+2 {
		t.Fatalf("installed masks = %d, want %d", common.Masks(), before+2)
	}
}

func TestClassicLogMaskErrors(t *testing.T) {
	common.ResetMasks()
	t.Cleanup(common.ResetMasks)

	cases := map[string]string{
		"invalid regex": `create log-mask bad
set bad.regex "^[unbalanced"
set bad.mask INFO
activate bad
`,
		"unknown level": `create log-mask bad
set bad.regex "^/"
set bad.mask NOISE
activate bad
`,
	}
	for name, text := range cases {
		if _, err := NewClassicParser().Parse(text); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

const minimalSocksProxy = `create interface ifsocks
set ifsocks.family IP
set ifsocks.host "localhost"
set ifsocks.port "0"
activate ifsocks
create proxy-socks s0
set s0.interface ifsocks
activate s0`

func TestParseSizeValues(t *testing.T) {
	cases := map[string]int64{
		"0":     0,
		"512":   512,
		"1kb":   1024,
		"128MB": 128 * 1024 * 1024,
		"1GB":   1024 * 1024 * 1024,
		"2tb":   2 * 1024 * 1024 * 1024 * 1024,
		"0x10":  16,
	}
	for in, want := range cases {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Fatalf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseSize("12qb"); err == nil {
		t.Fatalf("ParseSize(12qb) should fail")
	}
}

func TestParseJSONConfig(t *testing.T) {
	data := []byte(`{
	  "monitor": { "listen": { "host": "127.0.0.1", "port": 8401 } },
	  "proxies": [
	    {
	      "name": "tcp-xcodec-echo",
	      "listen": { "host": "127.0.0.1", "port": 8310 },
	      "upstream": { "host": "127.0.0.1", "port": 8311 },
	      "codec": { "mode": "xcodec", "role": "incoming" }
	    },
	    {
	      "name": "server-side",
	      "listen": { "host": "127.0.0.1", "port": 8312 },
	      "upstream": { "host": "127.0.0.1", "port": 8313 },
	      "codec": { "mode": "zlib+xcodec", "role": "outgoing", "compressorLevel": 9 }
	    }
	  ],
	  "socksProxies": [
	    { "name": "socks-demo", "listen": { "host": "127.0.0.1", "port": 8320 } }
	  ]
	}`)

	cfg, err := ParseJSON(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Proxies) != 2 || len(cfg.SocksProxies) != 1 || cfg.Monitor == nil {
		t.Fatalf("unexpected config shape: %+v", cfg)
	}

	p0 := cfg.Proxies[0]
	if p0.PeerCodec == nil || p0.InterfaceCodec != nil {
		t.Fatalf("incoming role must attach the codec to the peer slot")
	}
	if p0.PeerCodec.Mode != pipeline.ModeXCodec || p0.PeerCodec.Cache == nil {
		t.Fatalf("p0 codec = %+v", p0.PeerCodec)
	}

	p1 := cfg.Proxies[1]
	if p1.InterfaceCodec == nil || p1.PeerCodec != nil {
		t.Fatalf("outgoing role must attach the codec to the interface slot")
	}
	if p1.InterfaceCodec.Mode != pipeline.ModeZlibXCodec || p1.InterfaceCodec.CompressorLevel != 9 {
		t.Fatalf("p1 codec = %+v", p1.InterfaceCodec)
	}

	if cfg.Proxies[0].Interface.Family != proxy.FamilyIP || cfg.Proxies[0].Interface.Port != "8310" {
		t.Fatalf("p0 interface = %+v", cfg.Proxies[0].Interface)
	}
}

func TestParseJSONErrors(t *testing.T) {
	cases := map[string]string{
		"empty":           `{}`,
		"no name":         `{"proxies":[{"listen":{"host":"h","port":1},"upstream":{"host":"h","port":2}}]}`,
		"bad port":        `{"proxies":[{"name":"p","listen":{"host":"h","port":70000},"upstream":{"host":"h","port":2}}]}`,
		"bad mode":        `{"proxies":[{"name":"p","listen":{"host":"h","port":1},"upstream":{"host":"h","port":2},"codec":{"mode":"lz4"}}]}`,
		"bad level":       `{"proxies":[{"name":"p","listen":{"host":"h","port":1},"upstream":{"host":"h","port":2},"codec":{"mode":"zlib","compressorLevel":42}}]}`,
		"unknown field":   `{"proxies":[{"name":"p","listen":{"host":"h","port":1},"upstream":{"host":"h","port":2}}],"bogus":1}`,
		"empty host":      `{"proxies":[{"name":"p","listen":{"host":"","port":1},"upstream":{"host":"h","port":2}}]}`,
		"not json at all": `{{{`,
	}
	for name, data := range cases {
		if _, err := ParseJSON([]byte(data)); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestLoadDetectsFormat(t *testing.T) {
	dir := t.TempDir()

	jsonPath := dir + "/proxy.json"
	if err := writeFile(jsonPath, []byte(`{"proxies":[{"name":"p","listen":{"host":"127.0.0.1","port":0},"upstream":{"host":"127.0.0.1","port":1}}]}`)); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(jsonPath)
	if err != nil || len(cfg.Proxies) != 1 {
		t.Fatalf("json load: %v %+v", err, cfg)
	}

	confPath := dir + "/wanproxy.conf"
	if err := writeFile(confPath, []byte(strings.Replace(cppSampleConfig, "wanproxy-test.xcache", dir+"/w.xcache", 1))); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(confPath)
	if err != nil || len(cfg.Proxies) != 2 {
		t.Fatalf("classic load: %v %+v", err, cfg)
	}
	releaseDisk(t, dir+"/w.xcache")
}

func TestClassicDiskCachePathIsUnquoted(t *testing.T) {
	dir := t.TempDir()
	text := `create cache disk0
set disk0.type Disk
set disk0.size 4MB
set disk0.path "` + dir + `/sub.xcache"
activate disk0
create codec codec0
set codec0.codec XCodec
set codec0.cache disk0
activate codec0
create interface if0
set if0.family IP
set if0.host "127.0.0.1"
set if0.port "0"
activate if0
create peer peer0
set peer0.family IP
set peer0.host "127.0.0.1"
set peer0.port "1"
activate peer0
create proxy p0
set p0.type TCP-TCP
set p0.interface if0
set p0.peer peer0
set p0.peer_codec codec0
activate p0
`
	cfg, err := NewClassicParser().Parse(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	releaseDisk(t, dir+"/sub.xcache")
	if len(cfg.Caches) != 1 {
		t.Fatalf("caches = %d, want 1", len(cfg.Caches))
	}
	// The XCDFS volume file must be the unquoted path, truncated to
	// the configured size.
	st, err := os.Stat(dir + "/sub.xcache")
	if err != nil {
		t.Fatalf("disk cache volume was not created at the unquoted path: %v", err)
	}
	if st.Size() != 4*1024*1024 {
		t.Fatalf("volume size = %d, want 4MB", st.Size())
	}
	if _, err := os.Stat(`"` + dir + `/sub.xcache"`); err == nil {
		t.Fatalf("a quoted directory name was created")
	}
}

func writeTestHostKey(t *testing.T, dir string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	path := dir + "/ssh-server1.pem"
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClassicSSHProxy(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeTestHostKey(t, dir)

	text := `create interface if0
set if0.family IP
set if0.host "127.0.0.1"
set if0.port "0"
activate if0
create peer peer0
set peer0.family IP
set peer0.host "127.0.0.1"
set peer0.port "1"
activate peer0
create proxy sshproxy0
set sshproxy0.type SSH-SSH
set sshproxy0.interface if0
set sshproxy0.peer peer0
set sshproxy0.server_host_key "` + keyPath + `"
activate sshproxy0
`
	cfg, err := NewClassicParser().Parse(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Proxies) != 1 {
		t.Fatalf("proxies = %d", len(cfg.Proxies))
	}
	p := cfg.Proxies[0]
	if p.SSH == nil || p.SSH.HostKey == nil {
		t.Fatalf("SSH settings missing")
	}
	if p.SSH.HostKeyPath != keyPath {
		t.Fatalf("host key path = %q", p.SSH.HostKeyPath)
	}
}

func TestClassicSSHProxyDefaultKeyPath(t *testing.T) {
	// Without server_host_key the C++ version defaults to
	// ssh-server1.pem; a missing file must fail activation clearly.
	text := `create interface if0
set if0.family IP
set if0.host "127.0.0.1"
set if0.port "0"
activate if0
create peer peer0
set peer0.family IP
set peer0.host "127.0.0.1"
set peer0.port "1"
activate peer0
create proxy sshproxy0
set sshproxy0.type SSH-SSH
set sshproxy0.interface if0
set sshproxy0.peer peer0
activate sshproxy0
`
	if _, err := NewClassicParser().Parse(text); err == nil {
		t.Fatalf("expected error for missing default host key file")
	}
}

func TestClassicHostKeyRejectedForTCPProxy(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeTestHostKey(t, dir)
	text := `create interface if0
set if0.family IP
set if0.host "127.0.0.1"
set if0.port "0"
activate if0
create peer peer0
set peer0.family IP
set peer0.host "127.0.0.1"
set peer0.port "1"
activate peer0
create proxy p0
set p0.type TCP-TCP
set p0.interface if0
set p0.peer peer0
set p0.server_host_key "` + keyPath + `"
activate p0
`
	if _, err := NewClassicParser().Parse(text); err == nil {
		t.Fatalf("expected error for server_host_key on TCP proxy")
	}
}
