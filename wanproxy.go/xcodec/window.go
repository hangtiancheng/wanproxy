package xcodec

// Window is the XCodec backref window: a circular table of the most
// recently declared segment hashes, addressable by an 8-bit index.  It
// mirrors the C++ XCodecWindow.
type Window struct {
	slots   [WindowCount]uint64
	cursor  int
	present map[uint64]*windowEntry
}

type windowEntry struct {
	index   int
	segment []byte
}

// NewWindow returns an empty backref window.
func NewWindow() *Window {
	return &Window{present: make(map[uint64]*windowEntry)}
}

// Declare inserts a segment at the cursor, evicting whatever the cursor
// previously held.  It reports whether the hash was already present (a
// collision), in which case the old entry was cleared first, matching
// the C++ behaviour.
func (w *Window) Declare(hash uint64, segment []byte) bool {
	if hash == 0 {
		panic("xcodec: window hash must be non-zero")
	}
	if len(segment) != SegmentLength {
		panic("xcodec: window segment must be exactly 2048 bytes")
	}

	collision := false
	if old, ok := w.present[hash]; ok {
		collision = true
		w.slots[old.index] = 0
		delete(w.present, hash)
	}

	if old := w.slots[w.cursor]; old != 0 {
		delete(w.present, old)
	}

	w.slots[w.cursor] = hash
	w.present[hash] = &windowEntry{index: w.cursor, segment: cloneSegment(segment)}
	w.cursor = (w.cursor + 1) % WindowCount

	return collision
}

// Dereference returns the segment at window index c, or nil when the
// slot is empty.
func (w *Window) Dereference(c int) []byte {
	if c < 0 || c >= WindowCount {
		panic("xcodec: window index out of range")
	}
	if w.slots[c] == 0 {
		return nil
	}
	entry, ok := w.present[w.slots[c]]
	if !ok {
		return nil
	}
	return entry.segment
}

// Present reports whether the hash is in the window and, when data is
// non-nil, whether the window's segment matches it.  On success the
// window index is returned.
func (w *Window) Present(hash uint64, data []byte) (int, bool) {
	entry, ok := w.present[hash]
	if !ok {
		return 0, false
	}
	if data != nil && !segmentsEqual(entry.segment, data) {
		return 0, false
	}
	return entry.index, true
}

func segmentsEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
