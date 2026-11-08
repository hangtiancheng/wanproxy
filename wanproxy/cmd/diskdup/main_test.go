package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("could not write %s: %v", path, err)
	}
	return path
}

func openPair(t *testing.T, source, target string) (*os.File, *os.File) {
	t.Helper()
	src, err := os.Open(source)
	if err != nil {
		t.Fatalf("could not open source: %v", err)
	}
	tgt, err := os.OpenFile(target, os.O_RDWR, 0)
	if err != nil {
		src.Close()
		t.Fatalf("could not open target: %v", err)
	}
	t.Cleanup(func() { src.Close(); tgt.Close() })
	return src, tgt
}

func TestDupBlocksMinimalWrites(t *testing.T) {
	const block = 16
	source := make([]byte, 4*block)
	for i := range source {
		source[i] = byte(i)
	}
	target := make([]byte, 4*block)
	copy(target, source)
	for i := block; i < 2*block; i++ {
		target[i] ^= 0xff
	}
	for i := 3 * block; i < 4*block; i++ {
		target[i] ^= 0x55
	}

	src, tgt := openPair(t,
		writeTemp(t, "source", source),
		writeTemp(t, "target", target))

	stats, err := dupBlocks(discardLog(), src, tgt, block)
	if err != nil {
		t.Fatalf("dupBlocks failed: %v", err)
	}
	if stats.Compared != 4 {
		t.Fatalf("compared = %d, want 4", stats.Compared)
	}
	if stats.Written != 2 {
		t.Fatalf("written = %d, want 2 (only the differing blocks)", stats.Written)
	}

	got, err := os.ReadFile(tgt.Name())
	if err != nil {
		t.Fatalf("could not reread target: %v", err)
	}
	if string(got) != string(source) {
		t.Fatalf("target contents differ from source after dup")
	}
}

func TestDupBlocksIdenticalVolumesWriteNothing(t *testing.T) {
	const block = 64
	data := make([]byte, 8*block)
	for i := range data {
		data[i] = byte(i * 7)
	}
	src, tgt := openPair(t,
		writeTemp(t, "source", data),
		writeTemp(t, "target", data))

	stats, err := dupBlocks(discardLog(), src, tgt, block)
	if err != nil {
		t.Fatalf("dupBlocks failed: %v", err)
	}
	if stats.Compared != 8 || stats.Written != 0 {
		t.Fatalf("stats = %+v, want 8 compared and 0 written", stats)
	}
}

func TestDupBlocksStopsAtShorterTarget(t *testing.T) {
	const block = 32
	source := make([]byte, 4*block)
	for i := range source {
		source[i] = byte(i)
	}
	target := make([]byte, 2*block)
	copy(target, source[:2*block])

	src, tgt := openPair(t,
		writeTemp(t, "source", source),
		writeTemp(t, "target", target))

	stats, err := dupBlocks(discardLog(), src, tgt, block)
	if err != nil {
		t.Fatalf("dupBlocks failed: %v", err)
	}
	if stats.Compared != 2 || stats.Written != 0 {
		t.Fatalf("stats = %+v, want 2 compared and 0 written", stats)
	}
}

func TestDupBlocksPartialTrailingBlock(t *testing.T) {
	const block = 32
	source := make([]byte, 2*block+block/2)
	for i := range source {
		source[i] = byte(i)
	}
	target := make([]byte, len(source))

	src, tgt := openPair(t,
		writeTemp(t, "source", source),
		writeTemp(t, "target", target))

	stats, err := dupBlocks(discardLog(), src, tgt, block)
	if !errors.Is(err, errPartialBlock) {
		t.Fatalf("err = %v, want errPartialBlock", err)
	}
	if stats.Compared != 2 {
		t.Fatalf("compared = %d, want 2 full blocks before the partial one", stats.Compared)
	}
}

func TestRunCLI(t *testing.T) {
	const block = 16
	source := make([]byte, 3*block)
	for i := range source {
		source[i] = byte(i * 3)
	}
	target := make([]byte, 3*block)

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source")
	tgtPath := filepath.Join(dir, "target")
	if err := os.WriteFile(srcPath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tgtPath, target, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"-s", "16", srcPath, tgtPath}); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	got, err := os.ReadFile(tgtPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(source) {
		t.Fatalf("target does not match source after run")
	}
}

func TestRunUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"only-one-file"},
		{"a", "b", "c"},
		{"-s", "0", "a", "b"},
		{"-s", "not-a-number", "a", "b"},
		{"-x", "a", "b"},
	} {
		if err := run(args); !errors.Is(err, errUsage) {
			t.Fatalf("run(%q) err = %v, want errUsage", args, err)
		}
	}
}

func TestRunOpenErrors(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source")
	if err := os.WriteFile(srcPath, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{srcPath, filepath.Join(dir, "absent")}); err == nil {
		t.Fatalf("run with absent target should fail")
	}
	if err := run([]string{filepath.Join(dir, "absent"), srcPath}); err == nil {
		t.Fatalf("run with absent source should fail")
	}
}
