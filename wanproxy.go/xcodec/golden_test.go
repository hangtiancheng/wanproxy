package xcodec

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// The golden wire snapshots below pin the C++ wire format.

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func TestGoldenXCodecFrames(t *testing.T) {
	cases := map[string]struct {
		frame Frame
		hex   string
	}{
		"escape":  {EscapeFrame, "f100"},
		"ref":     {RefFrame(0x0123456789abcdef), "f1020123456789abcdef"},
		"backref": {BackrefFrame(7), "f10307"},
	}
	for name, tc := range cases {
		got := AppendFrame(nil, tc.frame)
		if !bytes.Equal(got, mustHex(t, tc.hex)) {
			t.Fatalf("%s: got %x, want %s", name, got, tc.hex)
		}
	}
}

func TestGoldenPipeFrames(t *testing.T) {
	var id CacheID
	for i := range id {
		id[i] = byte(i)
	}

	cases := map[string]struct {
		build func([]byte) []byte
		hex   string
	}{
		"hello":   {func(b []byte) []byte { return AppendHello(b, id) }, "ff10000102030405060708090a0b0c0d0e0f"},
		"frame":   {func(b []byte) []byte { return AppendPipeFrame(b, []byte{1, 2, 3}) }, "0200000003010203"},
		"ask":     {func(b []byte) []byte { return AppendAsk(b, []uint64{0x0123456789abcdef}) }, "f000010123456789abcdef"},
		"advance": {func(b []byte) []byte { return AppendAdvance(b, 3) }, "0100000003"},
		"eos":     {AppendEOS, "fc"},
		"eosAck":  {AppendEOSAck, "fb"},
	}
	for name, tc := range cases {
		got := tc.build(nil)
		if !bytes.Equal(got, mustHex(t, tc.hex)) {
			t.Fatalf("%s: got %x, want %s", name, got, tc.hex)
		}
	}
}

func TestGoldenSessionExchange(t *testing.T) {
	var id CacheID
	for i := range id {
		id[i] = 0xa0 + byte(i)
	}
	s := NewSession(NewMemoryCache(), WithSessionID(id))
	got := mustEncode(t, s, []byte("WANProxy"))
	want := "ff10a0a1a2a3a4a5a6a7a8a9aaabacadaeaf020000000857414e50726f7879"
	if !bytes.Equal(got, mustHex(t, want)) {
		t.Fatalf("session exchange: got %x, want %s", got, want)
	}
}

func TestPipeFrameReadRoundtrip(t *testing.T) {
	var id CacheID
	for i := range id {
		id[i] = byte(0x10 + i)
	}
	segment := repeatedSegment(0x42, 1)

	frames := []PipeFrame{
		{Kind: PipeFrameHello, ID: id},
		{Kind: PipeFrameFrame, Payload: []byte{1, 2, 3}},
		{Kind: PipeFrameAsk, Hashes: []uint64{5, 6}},
		{Kind: PipeFrameLearn, Segments: [][]byte{segment}},
		{Kind: PipeFrameAdvance, Count: 9},
		{Kind: PipeFrameEOS},
		{Kind: PipeFrameEOSAck},
	}

	var wire []byte
	for _, f := range frames {
		switch f.Kind {
		case PipeFrameHello:
			wire = AppendHello(wire, f.ID)
		case PipeFrameFrame:
			wire = AppendPipeFrame(wire, f.Payload)
		case PipeFrameAsk:
			wire = AppendAsk(wire, f.Hashes)
		case PipeFrameLearn:
			wire = AppendLearn(wire, f.Segments)
		case PipeFrameAdvance:
			wire = AppendAdvance(wire, f.Count)
		case PipeFrameEOS:
			wire = AppendEOS(wire)
		case PipeFrameEOSAck:
			wire = AppendEOSAck(wire)
		}
	}

	for i, want := range frames {
		frame, consumed, status, err := ReadPipeFrame(wire)
		if status != PipeFrameReadOK {
			t.Fatalf("frame %d: status=%v err=%v", i, status, err)
		}
		if frame.Kind != want.Kind {
			t.Fatalf("frame %d: kind = %v, want %v", i, frame.Kind, want.Kind)
		}
		switch want.Kind {
		case PipeFrameHello:
			if frame.ID != want.ID {
				t.Fatalf("frame %d: id mismatch", i)
			}
		case PipeFrameFrame:
			if !bytes.Equal(frame.Payload, want.Payload) {
				t.Fatalf("frame %d: payload mismatch", i)
			}
		case PipeFrameAsk:
			if len(frame.Hashes) != len(want.Hashes) {
				t.Fatalf("frame %d: hashes mismatch", i)
			}
		case PipeFrameLearn:
			if len(frame.Segments) != 1 || !bytes.Equal(frame.Segments[0], segment) {
				t.Fatalf("frame %d: segments mismatch", i)
			}
		case PipeFrameAdvance:
			if frame.Count != want.Count {
				t.Fatalf("frame %d: count mismatch", i)
			}
		}
		wire = wire[consumed:]
	}
	if len(wire) != 0 {
		t.Fatalf("leftover wire bytes: %d", len(wire))
	}
}
