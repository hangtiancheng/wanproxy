package xcodec

import (
	"bytes"
	"testing"
)

func repeatedSegment(b byte, count int) []byte {
	return bytes.Repeat([]byte{b}, SegmentLength*count)
}

func deterministicBytes(length int) []byte {
	out := make([]byte, length)
	for i := range out {
		out[i] = byte((i*31 + 17) & 0xff)
	}
	return out
}

func mustDecodeAll(t *testing.T, d *Decoder, encoded []byte) []byte {
	t.Helper()
	res := d.Decode(encoded)
	if res.Err != nil {
		t.Fatalf("decode error: %v", res.Err)
	}
	if len(res.Unknown) > 0 {
		t.Fatalf("decode blocked on unknown hashes: %v", res.Unknown)
	}
	if err := d.Finish(); err != nil {
		t.Fatalf("decode finish: %v", err)
	}
	return res.Output
}

// TestEncodeDecodeCharKAT ports the C++ xcodec-encode-decode1 test: for
// every byte value, 256 repeated segments must encode smaller and decode
// back exactly.
func TestEncodeDecodeCharKAT(t *testing.T) {
	for i := 0; i < 256; i++ {
		original := repeatedSegment(byte(i), 256)

		cache := NewMemoryCache()
		enc := NewEncoder(cache)
		encoded := enc.Encode(original)

		if len(encoded) == 0 {
			t.Fatalf("byte %d: empty output after encode", i)
		}
		if len(encoded) >= len(original) {
			t.Fatalf("byte %d: no reduction in size (%d >= %d)", i, len(encoded), len(original))
		}

		dec := NewDecoder(cache)
		decoded := mustDecodeAll(t, dec, encoded)
		if !bytes.Equal(decoded, original) {
			t.Fatalf("byte %d: roundtrip mismatch", i)
		}
	}
}

func TestEncodeDecodeEscapesMagic(t *testing.T) {
	cache := NewMemoryCache()
	enc := NewEncoder(cache)
	dec := NewDecoder(cache)

	if got := enc.Encode(nil); len(got) != 0 {
		t.Fatalf("encode(nil) = %v, want empty", got)
	}
	if got := mustDecodeAll(t, dec, nil); len(got) != 0 {
		t.Fatalf("decode(empty) = %v, want empty", got)
	}

	input := []byte{1, Magic, 2, Magic}
	encoded := enc.Encode(input)
	want := []byte{1, Magic, OpEscape, 2, Magic, OpEscape}
	if !bytes.Equal(encoded, want) {
		t.Fatalf("encoded = %v, want %v", encoded, want)
	}
	if got := mustDecodeAll(t, dec, encoded); !bytes.Equal(got, input) {
		t.Fatalf("decoded = %v, want %v", got, input)
	}
}

func TestEncodeDecodeRepeatedBytes(t *testing.T) {
	for _, b := range []byte{0, 1, 127, 128, 240, Magic, 255} {
		input := repeatedSegment(b, 32)
		cache := NewMemoryCache()
		enc := NewEncoder(cache)
		encoded := enc.Encode(input)
		if len(encoded) >= len(input) {
			t.Fatalf("byte %d: no reduction (%d >= %d)", b, len(encoded), len(input))
		}
		dec := NewDecoder(NewMemoryCache())
		if got := mustDecodeAll(t, dec, encoded); !bytes.Equal(got, input) {
			t.Fatalf("byte %d: roundtrip mismatch", b)
		}
	}
}

func TestEncodeDecodeDeterministicLengths(t *testing.T) {
	for _, length := range []int{1, 127, 2047, 2048, 2049, 4096, 65536} {
		input := deterministicBytes(length)
		cache := NewMemoryCache()
		enc := NewEncoder(cache)
		encoded := enc.Encode(input)
		dec := NewDecoder(NewMemoryCache())
		if got := mustDecodeAll(t, dec, encoded); !bytes.Equal(got, input) {
			t.Fatalf("length %d: roundtrip mismatch", length)
		}
	}
}

func TestEncoderReusesCacheAcrossCalls(t *testing.T) {
	cache := NewMemoryCache()
	enc := NewEncoder(cache)
	dec := NewDecoder(cache)

	first := repeatedSegment(7, 1)
	if got := mustDecodeAll(t, dec, enc.Encode(first)); !bytes.Equal(got, first) {
		t.Fatalf("first roundtrip mismatch")
	}

	second := repeatedSegment(7, 1)
	encoded := enc.Encode(second)
	// The whole second segment is a reference to the first.
	if len(encoded) != 10 {
		t.Fatalf("second encode length = %d, want 10 (MAGIC+REF+hash)", len(encoded))
	}
	if got := mustDecodeAll(t, dec, encoded); !bytes.Equal(got, second) {
		t.Fatalf("second roundtrip mismatch")
	}
}

func TestEncoderEmitsBackrefsOnlyWhenEnabled(t *testing.T) {
	input := repeatedSegment(9, 2)

	plain := NewEncoder(NewMemoryCache())
	encoded := plain.Encode(input)
	if len(encoded) != 2+SegmentLength+10 {
		t.Fatalf("plain encode length = %d, want EXTRACT + REF", len(encoded))
	}

	back := NewEncoder(NewMemoryCache(), WithBackrefs())
	encoded = back.Encode(input)
	if len(encoded) != 2+SegmentLength+3 {
		t.Fatalf("backref encode length = %d, want EXTRACT + BACKREF", len(encoded))
	}
	dec := NewDecoder(NewMemoryCache())
	if got := mustDecodeAll(t, dec, encoded); !bytes.Equal(got, input) {
		t.Fatalf("backref roundtrip mismatch")
	}
}

func TestEncoderFallsBackOnCacheCollision(t *testing.T) {
	input := repeatedSegment(3, 1)
	hash := HashSegment(input)

	cache := NewMemoryCache()
	cache.Enter(hash, repeatedSegment(4, 1))

	enc := NewEncoder(cache)
	encoded := enc.Encode(input)
	if len(encoded) < len(input) {
		t.Fatalf("collided input should not compress: %d < %d", len(encoded), len(input))
	}
	dec := NewDecoder(NewMemoryCache())
	if got := mustDecodeAll(t, dec, encoded); !bytes.Equal(got, input) {
		t.Fatalf("collision roundtrip mismatch")
	}
}

func TestDecoderReportsUnknownHash(t *testing.T) {
	dec := NewDecoder(NewMemoryCache())
	res := dec.Decode(AppendFrame(nil, RefFrame(1)))
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if len(res.Unknown) != 1 || res.Unknown[0] != 1 {
		t.Fatalf("unknown = %v, want [1]", res.Unknown)
	}

	// Feed the segment and resume.
	segment := repeatedSegment(5, 1)
	res = dec.Decode(nil)
	if len(res.Unknown) != 1 {
		t.Fatalf("still blocked, unknown = %v", res.Unknown)
	}
	dec.cache.Enter(1, segment)
	res = dec.Decode(nil)
	if res.Err != nil || len(res.Unknown) != 0 {
		t.Fatalf("resume failed: err=%v unknown=%v", res.Err, res.Unknown)
	}
	if !bytes.Equal(res.Output, segment) {
		t.Fatalf("resumed output mismatch")
	}
}

func TestDecoderSkimsAllUnknownHashes(t *testing.T) {
	cache := NewMemoryCache()
	dec := NewDecoder(cache)

	var stream []byte
	stream = AppendFrame(stream, RefFrame(11))
	stream = AppendFrame(stream, RefFrame(22))
	stream = AppendFrame(stream, RefFrame(11))
	stream = appendEscaped(stream, []byte("tail"))

	res := dec.Decode(stream)
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	want := []uint64{11, 22}
	if len(res.Unknown) != 2 || res.Unknown[0] != want[0] || res.Unknown[1] != want[1] {
		t.Fatalf("unknown = %v, want %v", res.Unknown, want)
	}
	if res.Consumed != 0 {
		t.Fatalf("consumed = %d, want 0 (blocked at first ref)", res.Consumed)
	}
}

func TestDecoderRejectsInvalidStreams(t *testing.T) {
	dec := NewDecoder(NewMemoryCache())
	if res := dec.Decode([]byte{Magic, 0xff}); res.Err == nil {
		t.Fatalf("expected unsupported opcode error")
	}

	dec = NewDecoder(NewMemoryCache())
	if res := dec.Decode(AppendFrame(nil, BackrefFrame(7))); res.Err == nil {
		t.Fatalf("expected backref window error")
	}
}

func TestDecoderPreservesPartialTrailingMagic(t *testing.T) {
	cache := NewMemoryCache()
	enc := NewEncoder(cache)
	dec := NewDecoder(cache)

	input := repeatedSegment(0x11, 2)
	encoded := enc.Encode(input)
	// Append a trailing magic byte split across two feeds.
	encoded = append(encoded, Magic)

	res := dec.Decode(encoded[:len(encoded)-1])
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	res2 := dec.Decode(encoded[len(encoded)-1:])
	if res2.Err != nil {
		t.Fatalf("unexpected error on continuation: %v", res2.Err)
	}
	if err := dec.Finish(); err == nil {
		t.Fatalf("expected incomplete stream at end")
	}
	output := append(res.Output, res2.Output...)
	if !bytes.Equal(output, input) {
		t.Fatalf("output mismatch with trailing magic pending")
	}
}

func TestDecoderExtractNameReuse(t *testing.T) {
	cache := NewMemoryCache()
	segment := repeatedSegment(0x21, 1)
	hash := HashSegment(segment)

	// Pre-poison the cache with different data under the same hash.
	cache.Enter(hash, repeatedSegment(0x22, 1))

	dec := NewDecoder(cache)
	stream := AppendFrame(nil, ExtractFrame(segment))
	res := dec.Decode(stream)
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if !bytes.Equal(res.Output, segment) {
		t.Fatalf("extract output mismatch")
	}
	if got := cache.Lookup(hash); !bytes.Equal(got, segment) {
		t.Fatalf("cache was not replaced on name reuse")
	}
}

func TestEncoderChunkedEqualsOneShot(t *testing.T) {
	input := deterministicBytes(SegmentLength * 64)

	cacheA := NewMemoryCache()
	oneShot := NewEncoder(cacheA).Encode(input)

	cacheB := NewMemoryCache()
	enc := NewEncoder(cacheB)
	var chunked []byte
	for off := 0; off < len(input); off += SegmentLength * 8 {
		end := off + SegmentLength*8
		if end > len(input) {
			end = len(input)
		}
		chunked = append(chunked, enc.Encode(input[off:end])...)
	}

	if !bytes.Equal(oneShot, chunked) {
		t.Fatalf("chunked encoding differs from one-shot encoding")
	}
}
