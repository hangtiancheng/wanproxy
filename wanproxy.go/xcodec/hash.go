package xcodec

import "math/bits"

// Hash transform tables: hashWordTable[ch] is the C++ `word`
// ((unsigned)ch + 1) and hashBitTable[ch] is the C++ `bit` (ffs(ch)).
// Precomputing them keeps the per-byte hot path down to table lookups
// and wrapping 32-bit arithmetic.
var hashWordTable, hashBitTable [256]uint32

func init() {
	for i := 0; i < 256; i++ {
		hashWordTable[i] = uint32(i) + 1
		hashBitTable[i] = ffs(byte(i))
	}
}

// ffs returns the 1-based index of the least significant set bit of ch,
// or zero when ch is zero, matching the POSIX ffs(3) semantics used by
// the C++ implementation.
func ffs(ch byte) uint32 {
	if ch == 0 {
		return 0
	}
	return uint32(bits.TrailingZeros8(ch)) + 1
}

// rollingSum mirrors one of the two C++ XCodecHash::RollingHash
// accumulators.  All arithmetic is 32-bit wrapping, exactly as in the
// C++ implementation.  The window bytes themselves live in the Hash (one
// raw-byte buffer shared by both accumulators, where the C++ version
// keeps a transformed uint32 buffer per accumulator); roll transforms
// the evicted byte through the same table that produced it.
type rollingSum struct {
	sum1 uint32 // Really <16-bit.
	sum2 uint32 // Really <32-bit.
}

func (s *rollingSum) add(word uint32) {
	s.sum1 += word
	s.sum2 += s.sum1
}

func (s *rollingSum) roll(word, dead uint32) {
	s.sum1 -= dead
	s.sum2 -= dead * SegmentLength

	s.sum1 += word
	s.sum2 += s.sum1
}

func (s *rollingSum) reset() {
	s.sum1 = 0
	s.sum2 = 0
}

// Hash is the XCodec rolling content hash.  It hashes exactly
// SegmentLength bytes; once the window is full it can be rolled forward
// one byte at a time.  The produced 64-bit digests are identical to the
// C++ XCodecHash.
type Hash struct {
	bytes  rollingSum
	bits   rollingSum
	buffer [SegmentLength]byte
	start  int
	length int
}

// NewHash returns a reset Hash.
func NewHash() *Hash {
	return &Hash{}
}

// Add feeds one byte into a not-yet-full hash window.  It panics if the
// window is already full, mirroring the C++ assertion.
func (h *Hash) Add(ch byte) {
	if h.length >= SegmentLength {
		panic("xcodec: hash window is already full")
	}

	h.buffer[h.start] = ch
	h.bytes.add(hashWordTable[ch])
	h.bits.add(hashBitTable[ch])

	h.length++
	h.start = (h.start + 1) % SegmentLength
}

// Reset empties the hash window.
func (h *Hash) Reset() {
	h.bytes.reset()
	h.bits.reset()

	h.length = 0
	h.start = 0
}

// Roll advances a full hash window by one byte, evicting the oldest.
// It panics if the window is not full, mirroring the C++ assertion.
func (h *Hash) Roll(ch byte) {
	if h.length != SegmentLength {
		panic("xcodec: hash window must be full before rolling")
	}

	dead := h.buffer[h.start]
	h.buffer[h.start] = ch

	h.bytes.roll(hashWordTable[ch], hashWordTable[dead])
	h.bits.roll(hashBitTable[ch], hashBitTable[dead])

	h.start = (h.start + 1) % SegmentLength
}

/*
 * XXX
 * Need to write a compression function for this; get rid of the
 * completely non-entropic bits, anyway, and try to mix the others.
 * (As in the C++ XCodecHash::mix.)
 */

// Mix compresses the internal state into a uint64 digest.  It panics if
// the window is not full, mirroring the C++ assertion.
func (h *Hash) Mix() uint64 {
	if h.length != SegmentLength {
		panic("xcodec: hash window must be full before mixing")
	}

	bitsHash := uint64(uint32((h.bits.sum1 << 16) + h.bits.sum2))
	bytesHash := uint64(uint32((h.bytes.sum1 << 20) + h.bytes.sum2))
	return (bitsHash << 36) + bytesHash
}

// HashSegment returns the XCodec digest of exactly one segment.  It
// panics if data is not SegmentLength bytes long.
func HashSegment(data []byte) uint64 {
	if len(data) != SegmentLength {
		panic("xcodec: segment must be exactly 2048 bytes")
	}

	h := Hash{}
	for i, ch := range data {
		h.buffer[i] = ch
		h.bytes.add(hashWordTable[ch])
		h.bits.add(hashBitTable[ch])
	}
	h.length = SegmentLength
	return h.Mix()
}
