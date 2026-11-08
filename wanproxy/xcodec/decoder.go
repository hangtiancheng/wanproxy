package xcodec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

type Decoder struct {
	cache       Cache
	window      *Window
	buf         []byte
	outputLimit int
}

func NewDecoder(cache Cache) *Decoder {
	return &Decoder{cache: cache, window: NewWindow()}
}

type DecodeResult struct {
	Output []byte

	Consumed int

	Unknown []uint64

	Err error
}

func (d *Decoder) Decode(chunk []byte) DecodeResult {
	before := len(d.buf)
	d.buf = append(d.buf, chunk...)

	var output []byte
	for len(d.buf) > 0 {
		off := bytes.IndexByte(d.buf, Magic)
		if off < 0 {
			if d.outputLimit > 0 && len(d.buf) > d.outputLimit-len(output) {
				return DecodeResult{Output: output, Err: ErrSessionLimit}
			}
			output = append(output, d.buf...)
			d.buf = d.buf[:0]
			break
		}
		if off > 0 {
			if d.outputLimit > 0 && off > d.outputLimit-len(output) {
				return DecodeResult{Output: output, Err: ErrSessionLimit}
			}
			output = append(output, d.buf[:off]...)
			d.buf = d.buf[off:]
		}

		if len(d.buf) == 1 {
			break
		}

		op := d.buf[1]
		expansion := 1
		if op == OpExtract || op == OpRef || op == OpBackref {
			expansion = SegmentLength
		}
		if d.outputLimit > 0 && expansion > d.outputLimit-len(output) {
			return DecodeResult{Output: output, Err: ErrSessionLimit}
		}
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
				unknown := d.skim(hash)
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

func (d *Decoder) Buffered() int { return len(d.buf) }

var ErrIncompleteStream = errors.New("incomplete XCodec frame at end of stream")

func (d *Decoder) Finish() error {
	if len(d.buf) > 0 {
		return ErrIncompleteStream
	}
	return nil
}

func (d *Decoder) skim(missing uint64) []uint64 {
	defined := make(map[uint64]bool)
	unknown := map[uint64]bool{missing: true}

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
