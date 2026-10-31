package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

func tackRun(t *testing.T, args []string, stdin []byte) (string, string, error) {
	t.Helper()
	opts, files := parseArgs(args)
	var stdout, stderr bytes.Buffer
	err := run(opts, files, bytes.NewReader(stdin), &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func repetitivePayload() []byte {
	block := bytes.Repeat([]byte("tack test block with some repetition. "), 64) // 2432 bytes
	var payload []byte
	for i := 0; i < 100; i++ {
		payload = append(payload, block...)
	}
	return payload
}

func TestTackCompressDecompressRoundtrip(t *testing.T) {
	payload := repetitivePayload()

	compressed, _, err := tackRun(t, []string{"-c"}, payload)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if len(compressed) >= len(payload) {
		t.Fatalf("no compression: %d >= %d", len(compressed), len(payload))
	}

	decompressed, _, err := tackRun(t, []string{"-d"}, []byte(compressed))
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if decompressed != string(payload) {
		t.Fatalf("roundtrip mismatch: %d vs %d bytes", len(decompressed), len(payload))
	}
}

func TestTackNullCache(t *testing.T) {
	payload := repetitivePayload()
	compressed, _, err := tackRun(t, []string{"-N", "-c"}, payload)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	// Out-of-band null cache: declarations become bare references, so
	// the stream is smaller than the in-band encoding but only decodes
	// with a cache that already knows the segments.
	inband, _, err := tackRun(t, []string{"-c"}, payload)
	if err != nil {
		t.Fatalf("inband compress: %v", err)
	}
	if len(compressed) >= len(inband) {
		t.Fatalf("null-cache encoding (%d) should beat in-band (%d)", len(compressed), len(inband))
	}
	if _, _, err := tackRun(t, []string{"-d"}, []byte(compressed)); err == nil {
		t.Fatalf("decoding a null-cache stream without the dictionary should fail")
	}
}

func TestTackPersistentCache(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "dict.cache")
	payload := repetitivePayload()

	// First compress builds the dictionary.
	first, _, err := tackRun(t, []string{"-p", cachePath, "-c"}, payload)
	if err != nil {
		t.Fatalf("first compress: %v", err)
	}

	// The persistent cache is out-of-band: even the first compress
	// emits bare references, and the second compress matches it.
	second, _, err := tackRun(t, []string{"-p", cachePath, "-c"}, payload)
	if err != nil {
		t.Fatalf("second compress: %v", err)
	}
	if len(second) != len(first) {
		t.Fatalf("second compress (%d) should match first (%d)", len(second), len(first))
	}
	inband, _, err := tackRun(t, []string{"-c"}, payload)
	if err != nil {
		t.Fatalf("inband compress: %v", err)
	}
	if len(first) >= len(inband) {
		t.Fatalf("out-of-band (%d) should beat in-band (%d)", len(first), len(inband))
	}

	// Decompress with the dictionary restores the payload.
	restored, _, err := tackRun(t, []string{"-p", cachePath, "-d"}, []byte(second))
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if restored != string(payload) {
		t.Fatalf("persistent roundtrip mismatch")
	}
}

func TestTackFIFOCache(t *testing.T) {
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "fifo.xcache")

	// Create the XCDFS volume up front (size detected from the file).
	disk, err := xcodec.OpenDisk(fifoPath, 4*1024*1024, nil)
	if err != nil {
		t.Fatalf("create volume: %v", err)
	}
	disk.Close()

	payload := repetitivePayload()
	compressed, _, err := tackRun(t, []string{"-F", fifoPath, "-c"}, payload)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	restored, _, err := tackRun(t, []string{"-F", fifoPath, "-d"}, []byte(compressed))
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if restored != string(payload) {
		t.Fatalf("fifo roundtrip mismatch")
	}
}

func TestTackHashes(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5a}, xcodec.SegmentLength*3+100)
	out, _, err := tackRun(t, []string{"-h"}, payload)
	if err != nil {
		t.Fatalf("hashes: %v", err)
	}
	if len(out) != 3*8 {
		t.Fatalf("hash output = %d bytes, want %d", len(out), 3*8)
	}
	want := xcodec.HashSegment(payload[:xcodec.SegmentLength])
	var got uint64
	for i := 0; i < 8; i++ {
		got = got<<8 | uint64(out[i])
	}
	if got != want {
		t.Fatalf("first hash = %016x, want %016x", got, want)
	}
}

func TestTackByteStats(t *testing.T) {
	payload := repetitivePayload()
	var stdout, stderr bytes.Buffer
	opts, files := parseArgs([]string{"-c", "-s"})
	if err := run(opts, files, bytes.NewReader(payload), &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stderr.String(), "uncompressed bytes") {
		t.Fatalf("expected byte stats on stderr, got %q", stderr.String())
	}
}

func TestTackTimingFlags(t *testing.T) {
	payload := repetitivePayload()

	// -T: summary stats for <total>.
	_, stderr, err := tackRun(t, []string{"-T", "-c"}, payload)
	if err != nil {
		t.Fatalf("-T: %v", err)
	}
	if !strings.Contains(stderr, "<total>") || !strings.Contains(stderr, "timer samples") {
		t.Fatalf("-T stats missing: %q", stderr)
	}

	// -T -S: raw samples.
	_, stderr, err = tackRun(t, []string{"-T", "-S", "-c"}, payload)
	if err != nil {
		t.Fatalf("-T -S: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "timer samples") {
			t.Fatalf("-S should print raw samples only: %q", stderr)
		}
	}
}

func TestTackUsageConstraints(t *testing.T) {
	cases := [][]string{
		{},                           // no action
		{"-h", "-s"},                 // hashes with stats
		{"-h", "-N"},                 // hashes with null cache
		{"-p", "x", "-N", "-c"},      // persist and null
		{"-F", "x", "-p", "y", "-c"}, // fifo and persist
		{"-E", "-c"},                 // each without timing
		{"-S", "-c"},                 // samples without timing
	}
	for _, args := range cases {
		if _, _, err := tackRun(t, args, nil); err == nil {
			t.Fatalf("%v: expected usage error", args)
		}
	}
}

func TestTackFileArguments(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "in.bin")
	payload := repetitivePayload()
	if err := os.WriteFile(inPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	compressed, _, err := tackRun(t, []string{"-c", inPath}, nil)
	if err != nil {
		t.Fatalf("compress file: %v", err)
	}
	restored, _, err := tackRun(t, []string{"-d"}, []byte(compressed))
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if restored != string(payload) {
		t.Fatalf("file roundtrip mismatch")
	}
}
