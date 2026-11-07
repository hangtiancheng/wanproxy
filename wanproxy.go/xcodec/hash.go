package xcodec

import "math/bits"

var hashWordTable, hashBitTable [256]uint32

func init() {
	for i := 0; i < 256; i++ {
		hashWordTable[i] = uint32(i) + 1
		hashBitTable[i] = ffs(byte(i))
	}
}

func ffs(ch byte) uint32 {
	if ch == 0 {
		return 0
	}
	return uint32(bits.TrailingZeros8(ch)) + 1
}

type rollingSum struct {
	sum1 uint32
	sum2 uint32
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

type Hash struct {
	bytes  rollingSum
	bits   rollingSum
	buffer [SegmentLength]byte
	start  int
	length int
}

func NewHash() *Hash {
	return &Hash{}
}

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

func (h *Hash) Reset() {
	h.bytes.reset()
	h.bits.reset()

	h.length = 0
	h.start = 0
}

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


func (h *Hash) Mix() uint64 {
	if h.length != SegmentLength {
		panic("xcodec: hash window must be full before mixing")
	}

	bitsHash := uint64(uint32((h.bits.sum1 << 16) + h.bits.sum2))
	bytesHash := uint64(uint32((h.bytes.sum1 << 20) + h.bytes.sum2))
	return (bitsHash << 36) + bytesHash
}

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
