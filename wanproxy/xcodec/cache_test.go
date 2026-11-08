package xcodec

import (
	"bytes"
	"testing"
)

func TestMemoryCacheLRUEviction(t *testing.T) {
	cache := NewMemoryCache(WithSegmentLimit(2))

	s1 := repeatedSegment(1, 1)
	s2 := repeatedSegment(2, 1)
	s3 := repeatedSegment(3, 1)
	h1, h2, h3 := HashSegment(s1), HashSegment(s2), HashSegment(s3)

	cache.Enter(h1, s1)
	cache.Enter(h2, s2)
	if got := cache.Lookup(h1); !bytes.Equal(got, s1) {
		t.Fatalf("lookup h1 mismatch")
	}
	cache.Enter(h3, s3)

	if cache.Has(h2) {
		t.Fatalf("h2 should have been evicted")
	}
	if !cache.Has(h1) || !cache.Has(h3) {
		t.Fatalf("h1 and h3 should remain")
	}
	if cache.Size() != 2 {
		t.Fatalf("size = %d, want 2", cache.Size())
	}
}

func TestMemoryCacheConnectIsolatesPeers(t *testing.T) {
	cache := NewMemoryCache(WithSegmentLimit(4))
	var idA, idB CacheID
	idA[0] = 1
	idB[0] = 2

	a := cache.Connect(idA)
	b := cache.Connect(idB)
	if a == b {
		t.Fatalf("distinct peers must get distinct caches")
	}
	if cache.Connect(idA) != a {
		t.Fatalf("same peer must reuse its cache")
	}

	seg := repeatedSegment(9, 1)
	hash := HashSegment(seg)
	a.Enter(hash, seg)
	if b.Has(hash) {
		t.Fatalf("peer B must not see peer A's segments")
	}
	if !a.Has(hash) {
		t.Fatalf("peer A lost its segment")
	}
}

func TestPairCachePromotesSecondaryHits(t *testing.T) {
	primary := NewMemoryCache()
	secondary := NewMemoryCache()
	pair := NewPairCache(primary, secondary)

	seg := repeatedSegment(0x77, 1)
	hash := HashSegment(seg)

	secondary.Enter(hash, seg)
	if primary.Has(hash) {
		t.Fatalf("primary should start empty")
	}
	if got := pair.Lookup(hash); !bytes.Equal(got, seg) {
		t.Fatalf("pair lookup mismatch")
	}
	if !primary.Has(hash) {
		t.Fatalf("secondary hit should be promoted into primary")
	}

	seg2 := repeatedSegment(0x78, 1)
	hash2 := HashSegment(seg2)
	pair.Enter(hash2, seg2)
	if !primary.Has(hash2) || !secondary.Has(hash2) {
		t.Fatalf("enter must populate both levels")
	}

	if pair.OutOfBand() {
		t.Fatalf("pair of in-band caches must be in-band")
	}
}

func TestNullCache(t *testing.T) {
	n := NewNullCache(CacheID{})
	seg := repeatedSegment(1, 1)
	hash := HashSegment(seg)
	n.Enter(hash, seg)
	if n.Has(hash) || n.Lookup(hash) != nil || n.Size() != 0 {
		t.Fatalf("null cache must store nothing")
	}
	if !n.OutOfBand() {
		t.Fatalf("null cache must be out-of-band")
	}
}

func TestOutOfBandEncoderEmitsReferences(t *testing.T) {
	cache := NewNullCache(CacheID{})
	enc := NewEncoder(cache)
	input := repeatedSegment(0x33, 2)
	encoded := enc.Encode(input)
	if len(encoded) != 20 {
		t.Fatalf("out-of-band encode length = %d, want two REFs", len(encoded))
	}

	segment := repeatedSegment(0x33, 1)
	shared := NewMemoryCache()
	shared.Enter(HashSegment(segment), segment)
	dec := NewDecoder(shared)
	if got := mustDecodeAll(t, dec, encoded); !bytes.Equal(got, input) {
		t.Fatalf("out-of-band roundtrip mismatch")
	}
}
