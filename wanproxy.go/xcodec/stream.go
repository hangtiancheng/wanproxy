package xcodec

import (
	"fmt"
	"io"
)

// streamChunk is the input accumulation size used by the streaming
// readers; it is a multiple of SegmentLength so chunk boundaries never
// force escapes, matching the TypeScript codec transforms.
const streamChunk = PipeMaxPayloadFrame

// EncodeReader wraps src and presents its XCodec-encoded form.  The
// encoder state (cache and backref window) persists for the whole
// stream; input is encoded in segment-aligned chunks and the final
// partial chunk is escaped on close, like the C++ encoder driven by
// tack.
type EncodeReader struct {
	src     io.Reader
	enc     *Encoder
	pending []byte
	inbuf   []byte
	srcEOF  bool
	err     error
}

// NewEncodeReader returns an encoding reader backed by cache.
func NewEncodeReader(src io.Reader, cache Cache) *EncodeReader {
	return &EncodeReader{src: src, enc: NewEncoder(cache)}
}

func (r *EncodeReader) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.srcEOF {
			if len(r.inbuf) > 0 {
				r.pending = r.enc.Encode(r.inbuf)
				r.inbuf = nil
				continue
			}
			r.err = io.EOF
			return 0, io.EOF
		}

		if cap(r.inbuf) < streamChunk {
			grown := make([]byte, len(r.inbuf), streamChunk)
			copy(grown, r.inbuf)
			r.inbuf = grown
		}
		n, rerr := r.src.Read(r.inbuf[len(r.inbuf):cap(r.inbuf)])
		r.inbuf = r.inbuf[:len(r.inbuf)+n]
		switch rerr {
		case nil:
		case io.EOF:
			r.srcEOF = true
		default:
			r.err = rerr
			return 0, r.err
		}

		full := len(r.inbuf)
		if !r.srcEOF {
			full -= len(r.inbuf) % SegmentLength
		}
		if full > 0 {
			r.pending = r.enc.Encode(r.inbuf[:full])
			r.inbuf = r.inbuf[full:]
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// Close releases the reader; the underlying source is not closed.
func (r *EncodeReader) Close() error { return nil }

// DecodeReader wraps src, which must hold an XCodec-encoded stream, and
// presents the decoded plaintext.
type DecodeReader struct {
	src     io.Reader
	dec     *Decoder
	pending []byte
	srcEOF  bool
	err     error
}

// NewDecodeReader returns a decoding reader backed by cache.
func NewDecodeReader(src io.Reader, cache Cache) *DecodeReader {
	return &DecodeReader{src: src, dec: NewDecoder(cache)}
}

func (r *DecodeReader) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.srcEOF {
			if r.dec.Buffered() > 0 {
				r.err = ErrIncompleteStream
			} else {
				r.err = io.EOF
			}
			return 0, r.err
		}

		buf := make([]byte, 65536)
		n, rerr := r.src.Read(buf)
		if n > 0 {
			res := r.dec.Decode(buf[:n])
			if res.Err != nil {
				r.err = res.Err
				return 0, r.err
			}
			if len(res.Unknown) > 0 {
				r.err = fmt.Errorf("xcodec: stream references %d unknown hashes", len(res.Unknown))
				return 0, r.err
			}
			r.pending = res.Output
		}
		switch rerr {
		case nil:
		case io.EOF:
			r.srcEOF = true
		default:
			r.err = rerr
			return 0, r.err
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// Close releases the reader; the underlying source is not closed.
func (r *DecodeReader) Close() error { return nil }
