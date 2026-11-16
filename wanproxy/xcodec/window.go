package xcodec

type Window struct {
	slots   [WindowCount]uint64
	valid   [WindowCount]bool
	cursor  int
	present map[uint64]*windowEntry
}

type windowEntry struct {
	index   int
	segment []byte
}

func NewWindow() *Window {
	return &Window{present: make(map[uint64]*windowEntry)}
}

func (w *Window) Declare(hash uint64, segment []byte) bool {
	if len(segment) != SegmentLength {
		panic("xcodec: window segment must be exactly 2048 bytes")
	}

	collision := false
	if old, ok := w.present[hash]; ok {
		collision = true
		w.valid[old.index] = false
		delete(w.present, hash)
	}

	if w.valid[w.cursor] {
		delete(w.present, w.slots[w.cursor])
	}

	w.slots[w.cursor] = hash
	w.valid[w.cursor] = true
	w.present[hash] = &windowEntry{index: w.cursor, segment: cloneSegment(segment)}
	w.cursor = (w.cursor + 1) % WindowCount

	return collision
}

func (w *Window) Dereference(c int) []byte {
	if c < 0 || c >= WindowCount {
		panic("xcodec: window index out of range")
	}
	if !w.valid[c] {
		return nil
	}
	entry, ok := w.present[w.slots[c]]
	if !ok {
		return nil
	}
	return entry.segment
}

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
