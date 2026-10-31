package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

func TestSizeRejectsNegativeAndOverflow(t *testing.T) {
	for _, input := range []string{"-1", "-1MB", "9223372036854775807KB", "8388608TB"} {
		if _, err := ParseSize(input); err == nil {
			t.Fatalf("accepted size %q", input)
		}
	}
}

func TestJSONRejectsTrailingContent(t *testing.T) {
	input := `{"socksProxies":[{"name":"s","listen":{"host":"127.0.0.1","port":0}}]}`
	for _, extra := range []string{"{}", "garbage", "null"} {
		if _, err := ParseJSON([]byte(input + extra)); err == nil {
			t.Fatalf("accepted trailing %q", extra)
		}
	}
	if _, err := ParseJSON([]byte(input + " \n")); err != nil {
		t.Fatal(err)
	}
}

func TestClassicValidatesCompressorAtLoad(t *testing.T) {
	input := "create codec c\nset c.compressor zlib\nset c.compressor_level 10\nactivate c"
	if _, err := Parse([]byte(input)); err == nil || !strings.Contains(err.Error(), "compressor") {
		t.Fatalf("invalid compressor accepted or reported too late: %v", err)
	}
}

func TestClassicUnixProxy(t *testing.T) {
	cfg, err := Parse([]byte(`create interface i
set i.family Unix
set i.path "/tmp/wanproxy-listen.sock"
activate i
create peer p
set p.family Unix
set p.path "/tmp/wanproxy-peer.sock"
activate p
create proxy x
set x.interface i
set x.peer p
activate x`))
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	if cfg.Proxies[0].Interface.Network() != "unix" {
		t.Fatal("lost Unix endpoint")
	}
}

func TestClassicClosesDiskOnParseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache")
	disk, err := xcodec.OpenDisk(path, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	local := disk.Local()
	input := fmt.Sprintf("create cache c\nset c.type Disk\nset c.path %q\nactivate c\ninvalid-command", path)
	if _, err := Parse([]byte(input)); err == nil {
		t.Fatal("expected parser failure")
	}
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}
	segment := make([]byte, xcodec.SegmentLength)
	local.Enter(xcodec.HashSegment(segment), segment)
	if local.Lookup(xcodec.HashSegment(segment)) != nil {
		t.Fatal("parser leaked its shared disk reference")
	}
}

type testCloser struct {
	calls *int
	err   error
}

func (c testCloser) Close() error { *c.calls++; return c.err }

func TestConfigCloseIsIdempotentAndKeepsErrors(t *testing.T) {
	calls := 0
	want := errors.New("close failure")
	cfg := &Config{}
	cfg.resources = append(cfg.resources, testCloser{&calls, want}, testCloser{&calls, nil})
	for range 2 {
		if err := cfg.Close(); !errors.Is(err, want) {
			t.Fatalf("close error: %v", err)
		}
	}
	if calls != 2 {
		t.Fatalf("closed resources %d times", calls)
	}
}
