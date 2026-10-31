package xcodec

import "encoding/binary"

// Encoder turns a view of a data stream into a series of references to
// other data, declarations of data to be referenced, and escaped literal
// data.  It is a port of the C++ XCodecEncoder; the produced byte stream
// is identical for identical inputs and cache states.
//
// The encoder keeps no state between Encode calls other than the cache
// and the backref window, so callers may feed a stream in arbitrary
// chunks (as tack and the pipe protocol do).
type Encoder struct {
	cache        Cache
	window       *Window
	stream       bool // in-band declarations unless the cache is out-of-band
	emitBackrefs bool
}

// EncoderOption customizes an Encoder.
type EncoderOption func(*Encoder)

// WithBackrefs lets the encoder emit <BACKREF> frames for segments that
// are still present in the backref window.  The C++ encoder never emits
// backrefs (decoders do support them); backref emission is off by
// default.
func WithBackrefs() EncoderOption {
	return func(e *Encoder) { e.emitBackrefs = true }
}

// NewEncoder returns an encoder backed by the given cache.
func NewEncoder(cache Cache, opts ...EncoderOption) *Encoder {
	e := &Encoder{
		cache:  cache,
		window: NewWindow(),
		stream: !cache.OutOfBand(),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Cache returns the encoder's backing cache.
func (e *Encoder) Cache() Cache { return e.cache }

// Encode encodes input, returning the encoded stream.
func (e *Encoder) Encode(input []byte) []byte {
	encoded, _ := e.EncodeWithReferences(input)
	return encoded
}

// candidate is a hash that may be declared if no earlier reference is
// found, mirroring the C++ candidate_symbol.
type candidate struct {
	set    bool
	offset int
	hash   uint64
}

// EncodeWithReferences encodes input and additionally returns the map of
// hashes to segments referenced by this stream (the C++ "refmap"), which
// the pipe protocol retains so that it can answer <ASK> messages until
// the peer <ADVANCE>s past the frames containing them.
func (e *Encoder) EncodeWithReferences(input []byte) ([]byte, map[uint64][]byte) {
	var output []byte
	refs := make(map[uint64][]byte)

	if len(input) == 0 {
		return output, refs
	}

	if len(input) < SegmentLength {
		return appendEscaped(output, input), refs
	}

	h := NewHash()
	var cand candidate
	consumed := 0 // input bytes already emitted to output
	fed := 0      // bytes fed to the hash since its last reset

	for pos := 0; pos < len(input); pos++ {
		// If we cannot acquire a complete hash within the remaining
		// input, stop looking.
		if fed+(len(input)-pos) < SegmentLength {
			break
		}

		if fed < SegmentLength {
			h.Add(input[pos])
			fed++
			if fed < SegmentLength {
				continue
			}
		} else {
			h.Roll(input[pos])
			fed++
		}

		start := pos + 1 - SegmentLength
		hash := h.Mix()

		// If there is a pending candidate hash that wouldn't overlap
		// with the data that the rolling hash presently covers,
		// declare it now.
		if cand.set && cand.offset+SegmentLength <= start {
			output = e.encodeDeclaration(output, input, consumed, cand.offset, cand.hash)
			consumed = cand.offset + SegmentLength
			cand.set = false
		}

		segment := input[start : start+SegmentLength]

		// Now attempt to encode this hash as a reference if it has
		// been defined before.
		if cached := e.cache.Lookup(hash); cached != nil {
			if !segmentsEqual(cached, segment) {
				// This hash isn't usable because it collides
				// with another, so keep looking for something
				// viable.  Do not mark it down as a candidate.
				continue
			}
			if start > consumed {
				output = appendEscaped(output, input[consumed:start])
			}
			output = e.encodeReference(output, hash, cached, refs)
			consumed = start + SegmentLength

			// Restart hashing after the referenced segment.
			h.Reset()
			fed = 0
			pos = consumed - 1
			cand.set = false
			continue
		}

		// Not defined before; it's a candidate for declaration if we
		// don't already have one that occurs earlier and covers data
		// included by this hash.
		if !cand.set {
			cand = candidate{set: true, offset: start, hash: hash}
		}
	}

	// There's a hash we can declare, do it.
	if cand.set {
		output = e.encodeDeclaration(output, input, consumed, cand.offset, cand.hash)
		consumed = cand.offset + SegmentLength
	}

	// There's data after that hash or no candidate hash, so just
	// escape it.
	if consumed < len(input) {
		output = appendEscaped(output, input[consumed:])
	}

	return output, refs
}

// encodeDeclaration mirrors XCodecEncoder::encode_declaration: escape the
// data preceding the declared segment, enter it into the cache and emit
// an in-band <EXTRACT>, or, for out-of-band caches, a <REF>.
func (e *Encoder) encodeDeclaration(output, input []byte, consumed, offset int, hash uint64) []byte {
	if offset > consumed {
		output = appendEscaped(output, input[consumed:offset])
	}

	segment := input[offset : offset+SegmentLength]
	e.cache.Enter(hash, segment)

	if !e.stream {
		// Declarations occur out-of-band.
		return e.encodeReference(output, hash, segment, nil)
	}

	// Declarations are extracted in-band.
	output = append(output, Magic, OpExtract)
	output = append(output, segment...)

	e.window.Declare(hash, segment)
	return output
}

// encodeReference mirrors XCodecEncoder::encode_reference.
func (e *Encoder) encodeReference(output []byte, hash uint64, segment []byte, refs map[uint64][]byte) []byte {
	if e.emitBackrefs {
		if idx, ok := e.window.Present(hash, segment); ok {
			return append(output, Magic, OpBackref, byte(idx))
		}
	}

	output = append(output, Magic, OpRef)
	var be [8]byte
	binary.BigEndian.PutUint64(be[:], hash)
	output = append(output, be[:]...)

	e.window.Declare(hash, segment)

	if refs != nil {
		if _, ok := refs[hash]; !ok {
			refs[hash] = cloneSegment(segment)
		}
	}
	return output
}

// appendEscaped mirrors XCodecEncoder::encode_escape: copy data, doubling
// every magic byte with an <ESCAPE> opcode.
func appendEscaped(output, data []byte) []byte {
	for _, b := range data {
		output = append(output, b)
		if b == Magic {
			output = append(output, OpEscape)
		}
	}
	return output
}
