package main

import (
	"bytes"
	"errors"
	"io"
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

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestTackReportsBufferedOutputErrors(t *testing.T) {
	want := errors.New("output unavailable")
	input := []byte("small output fails only on the final bufio flush")
	encoded := xcodec.NewEncoder(xcodec.NewMemoryCache()).Encode(input)
	for _, tc := range []struct {
		action fileAction
		input  []byte
	}{
		{actionCompress, input}, {actionDecompress, encoded},
		{actionHashes, bytes.Repeat([]byte{1}, xcodec.SegmentLength)},
	} {
		err := run(options{action: tc.action}, nil, bytes.NewReader(tc.input), failingWriter{want}, io.Discard)
		if !errors.Is(err, want) {
			t.Fatalf("action %d hid write failure: %v", tc.action, err)
		}
	}
}

type shortReader struct{ src io.Reader }

func (r shortReader) Read(p []byte) (int, error) { return r.src.Read(p[:min(len(p), 17)]) }

func TestTackHashesSurviveShortReads(t *testing.T) {
	input := bytes.Repeat([]byte{0x5a}, xcodec.SegmentLength*3+100)
	var got bytes.Buffer
	if err := hashes(shortReader{bytes.NewReader(input)}, &got, io.Discard, options{}, nil); err != nil {
		t.Fatal(err)
	}
	want, _, err := tackRun(t, []string{"-h"}, input)
	if err != nil || got.String() != want {
		t.Fatal("hashes depended on source Read boundaries")
	}
}

func repetitivePayload() []byte {
	block := bytes.Repeat([]byte("tack test block with some repetition. "), 64)
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

	first, _, err := tackRun(t, []string{"-p", cachePath, "-c"}, payload)
	if err != nil {
		t.Fatalf("first compress: %v", err)
	}

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

	_, stderr, err := tackRun(t, []string{"-T", "-c"}, payload)
	if err != nil {
		t.Fatalf("-T: %v", err)
	}
	if !strings.Contains(stderr, "<total>") || !strings.Contains(stderr, "timer samples") {
		t.Fatalf("-T stats missing: %q", stderr)
	}

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
		{},
		{"-h", "-s"},
		{"-h", "-N"},
		{"-p", "x", "-N", "-c"},
		{"-F", "x", "-p", "y", "-c"},
		{"-E", "-c"},
		{"-S", "-c"},
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
