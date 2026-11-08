package xcodec

import "encoding/binary"

type Encoder struct {
	cache        Cache
	window       *Window
	stream       bool
	emitBackrefs bool
}

type EncoderOption func(*Encoder)

func WithBackrefs() EncoderOption {
	return func(e *Encoder) { e.emitBackrefs = true }
}

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

func (e *Encoder) Cache() Cache { return e.cache }

func (e *Encoder) Encode(input []byte) []byte {
	encoded, _ := e.EncodeWithReferences(input)
	return encoded
}

type candidate struct {
	set    bool
	offset int
	hash   uint64
}

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
	consumed := 0
	fed := 0

	for pos := 0; pos < len(input); pos++ {
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

		if cand.set && cand.offset+SegmentLength <= start {
			output = e.encodeDeclaration(output, input, consumed, cand.offset, cand.hash, refs)
			consumed = cand.offset + SegmentLength
			cand.set = false
		}

		segment := input[start : start+SegmentLength]

		if cached := e.cache.Lookup(hash); cached != nil {
			if !segmentsEqual(cached, segment) {
				continue
			}
			if start > consumed {
				output = appendEscaped(output, input[consumed:start])
			}
			output = e.encodeReference(output, hash, cached, refs)
			consumed = start + SegmentLength

			h.Reset()
			fed = 0
			pos = consumed - 1
			cand.set = false
			continue
		}

		if !cand.set {
			cand = candidate{set: true, offset: start, hash: hash}
		}
	}

	if cand.set {
		output = e.encodeDeclaration(output, input, consumed, cand.offset, cand.hash, refs)
		consumed = cand.offset + SegmentLength
	}

	if consumed < len(input) {
		output = appendEscaped(output, input[consumed:])
	}

	return output, refs
}

func (e *Encoder) encodeDeclaration(output, input []byte, consumed, offset int, hash uint64, refs map[uint64][]byte) []byte {
	if offset > consumed {
		output = appendEscaped(output, input[consumed:offset])
	}

	segment := input[offset : offset+SegmentLength]
	e.cache.Enter(hash, segment)

	if !e.stream {
		return e.encodeReference(output, hash, segment, refs)
	}

	output = append(output, Magic, OpExtract)
	output = append(output, segment...)

	e.window.Declare(hash, segment)
	return output
}

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

func appendEscaped(output, data []byte) []byte {
	for _, b := range data {
		output = append(output, b)
		if b == Magic {
			output = append(output, OpEscape)
		}
	}
	return output
}
