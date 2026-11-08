package xcodec

import (
	"bytes"
	"errors"
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

type terminalReader struct {
	data []byte
	err  error
}

func (r *terminalReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, r.err
}

func TestStreamPreservesDataWithReadError(t *testing.T) {
	input := []byte("bytes returned together with a terminal error")
	encoded, err := io.ReadAll(NewEncodeReader(&terminalReader{input, io.ErrUnexpectedEOF}, NewMemoryCache()))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("encode error: %v", err)
	}
	decoded, err := io.ReadAll(NewDecodeReader(bytes.NewReader(encoded), NewMemoryCache()))
	if err != nil || !bytes.Equal(decoded, input) {
		t.Fatalf("encoded prefix lost: %q, %v", decoded, err)
	}
	decoded, err = io.ReadAll(NewDecodeReader(&terminalReader{encoded, io.ErrUnexpectedEOF}, NewMemoryCache()))
	if !errors.Is(err, io.ErrUnexpectedEOF) || !bytes.Equal(decoded, input) {
		t.Fatalf("decoded prefix lost: %q, %v", decoded, err)
	}
}

func TestStreamEmptyReadAndNoProgress(t *testing.T) {
	for _, r := range []io.Reader{
		NewEncodeReader(&terminalReader{}, NewMemoryCache()),
		NewDecodeReader(&terminalReader{}, NewMemoryCache()),
	} {
		if n, err := r.Read(nil); n != 0 || err != nil {
			t.Fatalf("empty read: %d, %v", n, err)
		}
		if _, err := r.Read(make([]byte, 1)); !errors.Is(err, io.ErrNoProgress) {
			t.Fatalf("no-progress read: %v", err)
		}
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
	enc := NewEncoder(NewNullCache(CacheID{}))
	input := repeatedSegment(0x44, 3)
	encoded := enc.Encode(input)

	dec := NewDecodeReader(bytes.NewReader(encoded), cache)
	if _, err := io.ReadAll(dec); err == nil {
		t.Fatalf("expected unknown-hash error")
	}
}
