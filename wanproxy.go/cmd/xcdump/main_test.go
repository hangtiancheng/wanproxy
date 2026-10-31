package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

func encodeStream(frames ...xcodec.Frame) []byte {
	var out []byte
	for _, f := range frames {
		out = xcodec.AppendFrame(out, f)
	}
	return out
}

func runDump(t *testing.T, input []byte, verbosity int) string {
	t.Helper()
	old := dumpVerbosity
	dumpVerbosity = verbosity
	defer func() { dumpVerbosity = old }()

	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	remainder := dumpFrames(w, input)
	if len(remainder) != 0 {
		printData(w, remainder)
	}
	w.Flush()
	return buf.String()
}

func TestDumpFrames(t *testing.T) {
	segment := bytes.Repeat([]byte{0x42}, xcodec.SegmentLength)
	hash := xcodec.HashSegment(segment)

	stream := []byte("raw data here")
	stream = append(stream, encodeStream(
		xcodec.EscapeFrame,
		xcodec.ExtractFrame(segment),
		xcodec.RefFrame(0x0123456789abcdef),
		xcodec.BackrefFrame(7),
	)...)
	stream = append(stream, []byte("trailing raw")...)

	got := runDump(t, stream, 1)
	lines := strings.Split(strings.TrimSpace(got), "\n")

	wantPrefix := []string{
		"<data/>",
		"<escape />",
		"<hash-declare",
		"<hash-reference",
		"<back-reference",
		"<data/>",
	}
	if len(lines) != len(wantPrefix) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(wantPrefix), got)
	}
	for i, want := range wantPrefix {
		if !strings.HasPrefix(lines[i], want) {
			t.Fatalf("line %d = %q, want prefix %q", i, lines[i], want)
		}
	}

	if !strings.Contains(got, "hash=\"0x0123456789abcdef\"") {
		t.Fatalf("missing ref hash: %s", got)
	}
	if !strings.Contains(got, "offset=\"7\"") {
		t.Fatalf("missing backref offset: %s", got)
	}
	if !strings.Contains(got, "hash=\"0x"+toHex64(hash)+"\"") {
		t.Fatalf("missing extract hash: %s", got)
	}
}

func TestDumpVerbosity(t *testing.T) {
	segment := bytes.Repeat([]byte{0x00}, xcodec.SegmentLength)
	stream := encodeStream(xcodec.ExtractFrame(segment), xcodec.RefFrame(1))

	quiet := runDump(t, stream, 0)
	if strings.Contains(quiet, "hash=") {
		t.Fatalf("verbosity 0 must not print hashes: %s", quiet)
	}

	verbose := runDump(t, stream, 1)
	if !strings.Contains(verbose, "hash=") || strings.Contains(verbose, "data=") {
		t.Fatalf("verbosity 1 must print hashes only: %s", verbose)
	}

	vverbose := runDump(t, stream, 2)
	if !strings.Contains(vverbose, "data=\"") {
		t.Fatalf("verbosity 2 must hexdump data: %s", vverbose[:80])
	}
}

func TestDumpPartialTrailingFrame(t *testing.T) {
	// A trailing incomplete frame is flushed as data.
	stream := encodeStream(xcodec.EscapeFrame)
	stream = append(stream, xcodec.Magic, xcodec.OpRef, 0x01, 0x02) // incomplete REF

	old := dumpVerbosity
	dumpVerbosity = 0
	defer func() { dumpVerbosity = old }()

	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	remainder := dumpFrames(w, stream)
	if len(remainder) == 0 {
		t.Fatalf("expected a remainder for the partial frame")
	}
	printData(w, remainder)
	w.Flush()

	got := buf.String()
	if !strings.HasPrefix(got, "<escape />") {
		t.Fatalf("missing escape: %q", got)
	}
	if !strings.Contains(got, "<data/>") {
		t.Fatalf("missing trailing data: %q", got)
	}
}

func toHex64(v uint64) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		out[i] = digits[v&0xf]
		v >>= 4
	}
	return string(out)
}
