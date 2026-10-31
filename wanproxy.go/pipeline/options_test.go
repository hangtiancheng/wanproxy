package pipeline

import (
	"bytes"
	"io"
	"reflect"
	"testing"
)

// TestResolveStepsCPPOrdering pins the C++ WANProxyCodecPipePair
// direction ordering: toward the WAN is encode-then-deflate, in from the
// WAN is inflate-then-decode.
func TestResolveStepsCPPOrdering(t *testing.T) {
	cases := []struct {
		mode Mode
		role Role
		dir  Direction
		want []Step
	}{
		{ModeZlibXCodec, RoleIncoming, DirectionSend, []Step{StepXCodecEncode, StepDeflate}},
		{ModeZlibXCodec, RoleIncoming, DirectionReceive, []Step{StepInflate, StepXCodecDecode}},
		{ModeZlibXCodec, RoleOutgoing, DirectionSend, []Step{StepInflate, StepXCodecDecode}},
		{ModeZlibXCodec, RoleOutgoing, DirectionReceive, []Step{StepXCodecEncode, StepDeflate}},
		{ModeXCodec, RoleIncoming, DirectionSend, []Step{StepXCodecEncode}},
		{ModeXCodec, RoleIncoming, DirectionReceive, []Step{StepXCodecDecode}},
		{ModeXCodec, RoleOutgoing, DirectionSend, []Step{StepXCodecDecode}},
		{ModeXCodec, RoleOutgoing, DirectionReceive, []Step{StepXCodecEncode}},
		{ModeZlib, RoleIncoming, DirectionSend, []Step{StepDeflate}},
		{ModeZlib, RoleIncoming, DirectionReceive, []Step{StepInflate}},
		{ModeZlib, RoleOutgoing, DirectionSend, []Step{StepInflate}},
		{ModeZlib, RoleOutgoing, DirectionReceive, []Step{StepDeflate}},
		{ModeNone, RoleIncoming, DirectionSend, []Step{StepPassThrough}},
		{ModeNone, RoleOutgoing, DirectionReceive, []Step{StepPassThrough}},
	}
	for _, tc := range cases {
		got := ResolveSteps(tc.mode, tc.role, tc.dir)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("ResolveSteps(%v, %v, %v) = %v, want %v", tc.mode, tc.role, tc.dir, got, tc.want)
		}
	}
}

func TestParseModeAndRole(t *testing.T) {
	modes := map[string]Mode{
		"none": ModeNone, "": ModeNone,
		"zlib":   ModeZlib,
		"xcodec": ModeXCodec, "XCodec": ModeXCodec,
		"zlib+xcodec": ModeZlibXCodec, "zlib-xcodec": ModeZlibXCodec,
	}
	for name, want := range modes {
		got, err := ParseMode(name)
		if err != nil || got != want {
			t.Fatalf("ParseMode(%q) = %v, %v", name, got, err)
		}
	}
	if _, err := ParseMode("bogus"); err == nil {
		t.Fatalf("ParseMode(bogus) should fail")
	}

	roles := map[string]Role{"incoming": RoleIncoming, "": RoleIncoming, "outgoing": RoleOutgoing}
	for name, want := range roles {
		got, err := ParseRole(name)
		if err != nil || got != want {
			t.Fatalf("ParseRole(%q) = %v, %v", name, got, err)
		}
	}
	if _, err := ParseRole("bogus"); err == nil {
		t.Fatalf("ParseRole(bogus) should fail")
	}
}

func TestZlibRoundtrip(t *testing.T) {
	input := bytes.Repeat([]byte("wanproxy"), 4096)

	var compressed bytes.Buffer
	w, err := NewDeflateWriter(&compressed, 6)
	if err != nil {
		t.Fatalf("deflate writer: %v", err)
	}
	if _, err := w.Write(input); err != nil {
		t.Fatalf("deflate write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("deflate close: %v", err)
	}
	if compressed.Len() >= len(input) {
		t.Fatalf("no compression: %d >= %d", compressed.Len(), len(input))
	}

	r, err := NewInflateReader(&compressed)
	if err != nil {
		t.Fatalf("inflate reader: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("inflate read: %v", err)
	}
	if !bytes.Equal(got, input) {
		t.Fatalf("zlib roundtrip mismatch")
	}
}

func TestDeflateWriterAcceptsCPlusPlusLevels(t *testing.T) {
	// C++ compressor_level defaults to -1 (zlib default) and accepts
	// 0..9; out-of-range values fall back to the default.
	for _, level := range []int{-1, 0, 1, 6, 9, 42, -7} {
		var buf bytes.Buffer
		w, err := NewDeflateWriter(&buf, level)
		if err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		if _, err := w.Write([]byte("data")); err != nil {
			t.Fatalf("level %d write: %v", level, err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("level %d close: %v", level, err)
		}
	}
}
