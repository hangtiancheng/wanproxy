package xcodec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// Decoder decodes an XCodec-encoded stream.  It is a port of the C++
// XCodecDecoder: encoded bytes are fed in via Decode, which consumes as
// much of the stream as it can and returns the decoded output.
//
// When a <REF> to an unknown hash is encountered the decoder stops,
// skims the remainder of the buffered stream for further unresolvable
// references (like the C++ decode_skim) and reports them, so the caller
// can interrogate a peer for all of them at once.  Once the hashes have
// been entered into the cache, a subsequent Decode call (possibly with an
// empty chunk) resumes decoding exactly where it stopped.
type Decoder struct {
	cache  Cache
	window *Window
	buf    []byte // encoded bytes not yet consumed
}

// NewDecoder returns a decoder backed by the given cache.
func NewDecoder(cache Cache) *Decoder {
	return &Decoder{cache: cache, window: NewWindow()}
}

// DecodeResult describes the outcome of one Decode call.
type DecodeResult struct {
	// Output is the decoded plaintext produced by this call.
	Output []byte

	// Consumed is the number of buffered encoded bytes consumed by
	// this call, including bytes from the chunk just fed in.
	Consumed int

	// Unknown lists the sorted hashes the decoder is blocked on, if
	// any.  They remain buffered; feed them to the cache (e.g. via a
	// pipe protocol <LEARN>) and call Decode again to resume.
	Unknown []uint64

	// Err is set when the stream is inconsistent or unrecoverable;
	// decoding cannot continue afterwards.
	Err error
}

// Decode feeds encoded bytes into the decoder and decodes as much of the
// stream as possible.
func (d *Decoder) Decode(chunk []byte) DecodeResult {
	before := len(d.buf)
	d.buf = append(d.buf, chunk...)

	var output []byte
	for len(d.buf) > 0 {
		// Everything before the next magic byte is literal data.
		off := bytes.IndexByte(d.buf, Magic)
		if off < 0 {
			output = append(output, d.buf...)
			d.buf = d.buf[:0]
			break
		}
		if off > 0 {
			output = append(output, d.buf[:off]...)
			d.buf = d.buf[off:]
		}

		// Need the opcode byte at least.
		if len(d.buf) == 1 {
			break
		}

		op := d.buf[1]
		switch op {
		case OpEscape:
			output = append(output, Magic)
			d.buf = d.buf[2:]

		case OpExtract:
			if len(d.buf) < 2+SegmentLength {
				goto done
			}
			segment := d.buf[2 : 2+SegmentLength]
			hash := HashSegment(segment)

			if oseg := d.cache.Lookup(hash); oseg != nil {
				if !segmentsEqual(oseg, segment) {
					// Name reuse in <EXTRACT>: replace the
					// old name with this one, as the C++
					// decoder does for in-band caches.
					d.cache.Replace(hash, segment)
				}
			} else {
				d.cache.Enter(hash, segment)
			}

			d.window.Declare(hash, segment)
			output = append(output, segment...)
			d.buf = d.buf[2+SegmentLength:]

		case OpRef:
			if len(d.buf) < 2+8 {
				goto done
			}
			hash := binary.BigEndian.Uint64(d.buf[2:10])

			oseg := d.cache.Lookup(hash)
			if oseg == nil {
				// Skim through the rest of the stream and
				// identify any other unresolvable references
				// so the caller can ask for them all at once.
				unknown := d.skim()
				return DecodeResult{
					Output:   output,
					Consumed: before + len(chunk) - len(d.buf),
					Unknown:  unknown,
				}
			}

			d.buf = d.buf[10:]
			d.window.Declare(hash, oseg)
			output = append(output, oseg...)

		case OpBackref:
			if len(d.buf) < 3 {
				goto done
			}
			idx := int(d.buf[2])
			oseg := d.window.Dereference(idx)
			if oseg == nil {
				return DecodeResult{
					Output:   output,
					Consumed: before + len(chunk) - len(d.buf),
					Err:      fmt.Errorf("index not present in <BACKREF> window: %d", idx),
				}
			}
			d.buf = d.buf[3:]
			output = append(output, oseg...)

		default:
			return DecodeResult{
				Output:   output,
				Consumed: before + len(chunk) - len(d.buf),
				Err:      fmt.Errorf("unsupported XCodec opcode %d", op),
			}
		}
	}
done:
	return DecodeResult{
		Output:   output,
		Consumed: before + len(chunk) - len(d.buf),
	}
}

// Buffered returns the number of encoded bytes awaiting consumption,
// either because a frame is incomplete or because decoding is blocked on
// unknown hashes.
func (d *Decoder) Buffered() int { return len(d.buf) }

// ErrIncompleteStream is reported when an encoded stream ends with a
// partial frame still buffered.
var ErrIncompleteStream = errors.New("incomplete XCodec frame at end of stream")

// Finish reports whether the stream ended cleanly.  It returns
// ErrIncompleteStream when encoded bytes remain buffered.
func (d *Decoder) Finish() error {
	if len(d.buf) > 0 {
		return ErrIncompleteStream
	}
	return nil
}

// skim mirrors the C++ decode_skim: walk the buffered remainder of the
// stream without consuming it and collect every referenced hash that is
// neither in the cache nor declared by an <EXTRACT> earlier in the
// remainder.
func (d *Decoder) skim() []uint64 {
	defined := make(map[uint64]bool)
	unknown := make(map[uint64]bool)

	buf := d.buf
	for len(buf) > 0 {
		off := bytes.IndexByte(buf, Magic)
		if off < 0 {
			break
		}
		buf = buf[off:]
		if len(buf) == 1 {
			break
		}

		switch buf[1] {
		case OpEscape:
			buf = buf[2:]
		case OpExtract:
			if len(buf) < 2+SegmentLength {
				return sortedHashes(unknown)
			}
			hash := HashSegment(buf[2 : 2+SegmentLength])
			defined[hash] = true
			buf = buf[2+SegmentLength:]
		case OpRef:
			if len(buf) < 10 {
				return sortedHashes(unknown)
			}
			hash := binary.BigEndian.Uint64(buf[2:10])
			if !d.cache.Has(hash) && !defined[hash] {
				unknown[hash] = true
			}
			buf = buf[10:]
		case OpBackref:
			if len(buf) < 3 {
				return sortedHashes(unknown)
			}
			buf = buf[3:]
		default:
			return sortedHashes(unknown)
		}
	}
	return sortedHashes(unknown)
}

func sortedHashes(set map[uint64]bool) []uint64 {
	if len(set) == 0 {
		return nil
	}
	out := make([]uint64, 0, len(set))
	for hash := range set {
		out = append(out, hash)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
