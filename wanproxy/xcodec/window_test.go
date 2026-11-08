package xcodec

import (
	"bytes"
	"testing"
)

func TestWindowDeclareAndDereference(t *testing.T) {
	w := NewWindow()
	seg := repeatedSegment(0x10, 1)
	hash := HashSegment(seg)

	if w.Dereference(0) != nil {
		t.Fatalf("empty slot must dereference to nil")
	}

	if collision := w.Declare(hash, seg); collision {
		t.Fatalf("first declare must not collide")
	}
	if got := w.Dereference(0); !bytes.Equal(got, seg) {
		t.Fatalf("dereference mismatch")
	}
	if idx, ok := w.Present(hash, seg); !ok || idx != 0 {
		t.Fatalf("present = (%d, %v), want (0, true)", idx, ok)
	}

	if collision := w.Declare(hash, seg); !collision {
		t.Fatalf("redeclare must report collision")
	}
	if idx, ok := w.Present(hash, nil); !ok || idx != 1 {
		t.Fatalf("present after redeclare = (%d, %v), want (1, true)", idx, ok)
	}
	if w.Dereference(0) != nil {
		t.Fatalf("old slot must be cleared")
	}
}

func TestWindowWrapsAround(t *testing.T) {
	w := NewWindow()
	segs := make([][]byte, WindowCount)
	hashes := make([]uint64, WindowCount)
	for i := 0; i < WindowCount; i++ {
		segs[i] = repeatedSegment(byte(i), 1)
		hashes[i] = HashSegment(segs[i])
		w.Declare(hashes[i], segs[i])
	}

	extra := repeatedSegment(0xff, 1)
	extraHash := HashSegment(extra)
	w.Declare(extraHash, extra)

	if w.Dereference(0) == nil {
		t.Fatalf("slot 0 should now hold the extra segment")
	}
	if got := w.Dereference(0); !bytes.Equal(got, extra) {
		t.Fatalf("slot 0 mismatch")
	}
	if _, ok := w.Present(hashes[0], nil); ok {
		t.Fatalf("evicted hash must not be present")
	}
	if idx, ok := w.Present(hashes[1], nil); !ok || idx != 1 {
		t.Fatalf("hash 1 must remain at index 1")
	}
}

func TestWindowPresentVerifiesData(t *testing.T) {
	w := NewWindow()
	seg := repeatedSegment(0x20, 1)
	hash := HashSegment(seg)
	w.Declare(hash, seg)

	if _, ok := w.Present(hash, repeatedSegment(0x21, 1)); ok {
		t.Fatalf("present must fail when data differs")
	}
	if _, ok := w.Present(hash, seg); !ok {
		t.Fatalf("present must succeed for matching data")
	}
}
