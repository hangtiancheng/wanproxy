package xcodec

import (
	"bytes"
	"testing"
)


var benchSinkHash uint64


func BenchmarkHashRoll(b *testing.B) {
	data := deterministicBytes(64 * 1024)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h := NewHash()
		for _, ch := range data[:SegmentLength] {
			h.Add(ch)
		}
		for _, ch := range data[SegmentLength:] {
			h.Roll(ch)
		}
		benchSinkHash += h.Mix()
	}
}

func benchRepeatingData(size int) []byte {
	base := deterministicBytes(64 * 1024)
	out := make([]byte, size)
	for i := 0; i < size; i += len(base) {
		copy(out[i:], base)
	}
	return out
}

func BenchmarkEncode(b *testing.B) {
	data := benchRepeatingData(1024 * 1024)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		enc := NewEncoder(NewMemoryCache())
		benchSinkHash += uint64(len(enc.Encode(data)))
	}
}

func BenchmarkEncodeReference(b *testing.B) {
	data := benchRepeatingData(1024 * 1024)
	cache := NewMemoryCache()
	enc := NewEncoder(cache)
	enc.Encode(data)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSinkHash += uint64(len(enc.Encode(data)))
	}
}

func BenchmarkDecode(b *testing.B) {
	data := benchRepeatingData(1024 * 1024)
	cache := NewMemoryCache()
	encoded := NewEncoder(cache).Encode(data)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dec := NewDecoder(cache)
		res := dec.Decode(encoded)
		if res.Err != nil || len(res.Unknown) != 0 {
			b.Fatalf("decode failed: %v (unknown %d)", res.Err, len(res.Unknown))
		}
		benchSinkHash += uint64(len(res.Output))
	}
}

func BenchmarkEncodeDecodeRoundtrip(b *testing.B) {
	data := benchRepeatingData(1024 * 1024)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache := NewMemoryCache()
		encoded := NewEncoder(cache).Encode(data)
		res := NewDecoder(cache).Decode(encoded)
		if res.Err != nil || !bytes.Equal(res.Output, data) {
			b.Fatalf("roundtrip mismatch")
		}
	}
}

func BenchmarkSessionRoundtrip(b *testing.B) {
	data := benchRepeatingData(1024 * 1024)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := NewSession(NewMemoryCache())
		bSession := NewSession(NewMemoryCache())

		wire, err := a.EncodeData(data)
		if err != nil {
			b.Fatal(err)
		}
		eos, err := a.CloseWrite()
		if err != nil {
			b.Fatal(err)
		}
		wire = append(wire, eos...)

		var output []byte
		for len(wire) > 0 {
			res, err := bSession.Receive(wire)
			wire = nil
			if err != nil {
				b.Fatal(err)
			}
			output = append(output, res.Output...)
			if len(res.Responses) > 0 {
				resA, err := a.Receive(res.Responses)
				if err != nil {
					b.Fatal(err)
				}
				wire = append(wire, resA.Responses...)
			}
		}
		if !bytes.Equal(output, data) {
			b.Fatalf("session roundtrip mismatch: %d vs %d bytes", len(output), len(data))
		}
	}
}
