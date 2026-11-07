package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

func TestClosesDiskOnParseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache")
	disk, err := xcodec.OpenDisk(path, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	local := disk.Local()
	fields := fmt.Sprintf(`"caches":{"disk":{"type":"disk","path":%q}},"codecs":{"bad":{"mode":"xcodec","cache":"missing"}},`, path)
	if _, err := Parse([]byte(strings.Replace(minimalConfig, `{`, `{`+fields, 1))); err == nil {
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
