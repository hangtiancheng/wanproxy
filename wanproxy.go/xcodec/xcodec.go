// Package xcodec is a Go port of the WANProxy XCodec codec
// (https://github.com/wanproxy/wanproxy, xcodec/).
//
// XCodec performs stream deduplication: it turns a data stream into a series
// of references to previously seen 2048-byte segments, declarations of new
// segments, and escaped literal data.  The wire encoding is byte-for-byte
// compatible with the C++ implementation.
package xcodec

// Magic is the escape byte that introduces every XCodec control frame.
const Magic byte = 0xf1

// XCodec stream opcodes.
const (
	// OpEscape: <MAGIC> <OP_ESCAPE> emits a literal Magic byte.
	OpEscape byte = 0x00

	// OpExtract: <MAGIC> <OP_EXTRACT> data[SegmentLength] declares a new
	// segment in-band; the data is hashed and inserted into the output
	// stream and into the backref window.
	OpExtract byte = 0x01

	// OpRef: <MAGIC> <OP_REF> hash[uint64 big-endian] references a
	// previously declared segment by hash.
	OpRef byte = 0x02

	// OpBackref: <MAGIC> <OP_BACKREF> index[uint8] references a segment
	// by its position in the backref window.
	OpBackref byte = 0x03
)

// SegmentLength is the size, in bytes, of an XCodec segment.
const SegmentLength = 2048

// WindowMax is the highest backref index; WindowCount is the number of
// entries in the backref window.
const (
	WindowMax   = 0xff
	WindowCount = WindowMax + 1
)

// Codec bundles a cache with the codec configuration, mirroring the C++
// XCodec class.
type Codec struct {
	cache Cache
}

// NewCodec returns a Codec backed by the given cache.
func NewCodec(cache Cache) *Codec {
	return &Codec{cache: cache}
}

// Cache returns the codec's backing cache.
func (c *Codec) Cache() Cache {
	return c.cache
}
