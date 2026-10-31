package payload

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"io"
	"testing"
)

func TestTextPatternProperties(t *testing.T) {
	const size = 4 << 20
	a, err := SHA256(Text, size, 42)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SHA256(Text, size, 42)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("text pattern is not deterministic")
	}
	c, _ := SHA256(Text, size, 43)
	if a == c {
		t.Fatal("different seeds must differ")
	}

	data := make([]byte, size)
	if n, err := io.ReadFull(NewReader(Text, size, 42), data); err != nil || n != size {
		t.Fatalf("read %d/%d: %v", n, size, err)
	}

	// zlib compressibility
	var zbuf bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&zbuf, 6)
	zw.Write(data)
	zw.Close()
	t.Logf("zlib ratio: %.3f (%d -> %d)", float64(zbuf.Len())/float64(size), size, zbuf.Len())

	// Duplicate 2048-byte windows at any offset (what XCodec's rolling
	// hash can turn into references), sampled every 256 bytes.
	seen := map[[32]byte]int{}
	dup, total := 0, 0
	h := sha256.New()
	for i := 0; i+2048 <= size; i += 256 {
		h.Reset()
		h.Write(data[i : i+2048])
		var sum [32]byte
		copy(sum[:], h.Sum(nil))
		total++
		if seen[sum] > 0 {
			dup++
		}
		seen[sum]++
	}
	t.Logf("duplicate 2048B windows: %d/%d (%.1f%%)", dup, total, 100*float64(dup)/float64(total))
	if zbuf.Len() > size/4 {
		t.Errorf("text should compress below 25%%, got %d", zbuf.Len())
	}
	if dup == 0 {
		t.Errorf("text should contain duplicate segments for dedup")
	}
}
