package xcodec

import (
	"bytes"
	"io"
	"testing"
)

func TestStreamRoundtrip(t *testing.T) {
	input := deterministicBytes(SegmentLength*100 + 1234)

	encCache := NewMemoryCache()
	decCache := NewMemoryCache()

	encoded := NewEncodeReader(bytes.NewReader(input), encCache)
	decoded := NewDecodeReader(encoded, decCache)

	got, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, input) {
		t.Fatalf("stream roundtrip mismatch: %d vs %d bytes", len(got), len(input))
	}
}

func TestStreamRoundtripTinyCache(t *testing.T) {
	input := deterministicBytes(SegmentLength * 50)

	encCache := NewMemoryCache(WithSegmentLimit(3))
	decCache := NewMemoryCache(WithSegmentLimit(3))

	encoded := NewEncodeReader(bytes.NewReader(input), encCache)
	decoded := NewDecodeReader(encoded, decCache)

	got, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, input) {
		t.Fatalf("tiny-cache stream roundtrip mismatch")
	}
}

func TestStreamEncodeMatchesOneShot(t *testing.T) {
	input := deterministicBytes(SegmentLength*40 + 999)

	cacheA := NewMemoryCache()
	oneShot := NewEncoder(cacheA).Encode(input)

	cacheB := NewMemoryCache()
	streamed, err := io.ReadAll(NewEncodeReader(bytes.NewReader(input), cacheB))
	if err != nil {
		t.Fatalf("stream encode: %v", err)
	}
	if !bytes.Equal(oneShot, streamed) {
		t.Fatalf("streamed encoding differs from one-shot")
	}
}

func TestDecodeReaderRejectsUnknownRefs(t *testing.T) {
	cache := NewMemoryCache()
	enc := NewEncoder(NewNullCache(CacheID{})) // out-of-band: bare REFs
	input := repeatedSegment(0x44, 3)
	encoded := enc.Encode(input)

	dec := NewDecodeReader(bytes.NewReader(encoded), cache)
	if _, err := io.ReadAll(dec); err == nil {
		t.Fatalf("expected unknown-hash error")
	}
}
