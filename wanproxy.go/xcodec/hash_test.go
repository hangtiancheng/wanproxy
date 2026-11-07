package xcodec

import (
	"strconv"
	"testing"
)

func TestHashKnownAnswer(t *testing.T) {
	seg := make([]byte, SegmentLength)
	for b := 0; b < 256; b++ {
		for i := range seg {
			seg[i] = byte(b)
		}
		want, err := strconv.ParseUint(charKatHex[b][2:], 16, 64)
		if err != nil {
			t.Fatalf("bad KAT vector %d: %v", b, err)
		}
		if got := HashSegment(seg); got != want {
			t.Fatalf("hash of segment filled with 0x%02x = 0x%016x, want 0x%016x", b, got, want)
		}
	}
}

func TestHashSegmentMatchesIncrementalAdd(t *testing.T) {
	segment := make([]byte, SegmentLength)
	for i := range segment {
		segment[i] = byte((i*17 + 3) & 0xff)
	}
	h := NewHash()
	for _, ch := range segment {
		h.Add(ch)
	}
	if got, want := h.Mix(), HashSegment(segment); got != want {
		t.Fatalf("incremental mix = 0x%016x, want 0x%016x", got, want)
	}
}

func TestHashRollMatchesFreshComputation(t *testing.T) {
	data := make([]byte, SegmentLength+64)
	for i := range data {
		data[i] = byte((i*29 + 7) & 0xff)
	}
	h := NewHash()
	for _, ch := range data[:SegmentLength] {
		h.Add(ch)
	}
	for i := SegmentLength; i < len(data); i++ {
		h.Roll(data[i])
		want := HashSegment(data[i-SegmentLength+1 : i+1])
		if got := h.Mix(); got != want {
			t.Fatalf("rolled hash at %d = 0x%016x, want 0x%016x", i, got, want)
		}
	}
}

func TestHashPanicsOnInvalidUse(t *testing.T) {
	cases := []struct {
		name string
		fn   func()
	}{
		{"mix before full", func() { NewHash().Mix() }},
		{"roll before full", func() { NewHash().Roll(1) }},
		{"add after full", func() {
			h := NewHash()
			for i := 0; i < SegmentLength; i++ {
				h.Add(0)
			}
			h.Add(0)
		}},
		{"hash short segment", func() { HashSegment(make([]byte, 1)) }},
	}
	for _, tc := range cases {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s: expected panic", tc.name)
				}
			}()
			tc.fn()
		}()
	}

	h := NewHash()
	for i := 0; i < SegmentLength; i++ {
		h.Add(0)
	}
	h.Reset()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatalf("mix after reset: expected panic")
			}
		}()
		h.Mix()
	}()
}

func BenchmarkHashSegment(b *testing.B) {
	seg := make([]byte, SegmentLength)
	for i := range seg {
		seg[i] = byte(i)
	}
	b.SetBytes(SegmentLength)
	for i := 0; i < b.N; i++ {
		HashSegment(seg)
	}
}
