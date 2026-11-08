package xcodec

import (
	"fmt"
	"io"
)

const streamChunk = PipeMaxPayloadFrame

type EncodeReader struct {
	src     io.Reader
	enc     *Encoder
	pending []byte
	inbuf   []byte
	srcEOF  bool
	err     error
}

func NewEncodeReader(src io.Reader, cache Cache) *EncodeReader {
	return &EncodeReader{src: src, enc: NewEncoder(cache)}
}

func (r *EncodeReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	emptyReads := 0
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
		if n == 0 && rerr == nil {
			emptyReads++
			if emptyReads >= 100 {
				rerr = io.ErrNoProgress
			}
		} else {
			emptyReads = 0
		}
		switch rerr {
		case nil:
		case io.EOF:
			r.srcEOF = true
		default:
			r.err = rerr
			r.srcEOF = true
		}

		full := len(r.inbuf)
		if !r.srcEOF {
			full -= len(r.inbuf) % SegmentLength
		}
		if full > 0 {
			r.pending = r.enc.Encode(r.inbuf[:full])
			r.inbuf = r.inbuf[:copy(r.inbuf, r.inbuf[full:])]
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (r *EncodeReader) Close() error { return nil }

type DecodeReader struct {
	src     io.Reader
	dec     *Decoder
	pending []byte
	srcEOF  bool
	err     error
	buf     [65536]byte
}

func NewDecodeReader(src io.Reader, cache Cache) *DecodeReader {
	return &DecodeReader{src: src, dec: NewDecoder(cache)}
}

func (r *DecodeReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	emptyReads := 0
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

		n, rerr := r.src.Read(r.buf[:])
		if n == 0 && rerr == nil {
			emptyReads++
			if emptyReads >= 100 {
				rerr = io.ErrNoProgress
			}
		} else {
			emptyReads = 0
		}
		if n > 0 {
			res := r.dec.Decode(r.buf[:n])
			r.pending = res.Output
			if res.Err != nil {
				r.err = res.Err
			} else if len(res.Unknown) > 0 {
				r.err = fmt.Errorf("xcodec: stream references %d unknown hashes", len(res.Unknown))
			}
		}
		switch rerr {
		case nil:
		case io.EOF:
			r.srcEOF = true
		default:
			if r.err == nil {
				r.err = rerr
			}
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (r *DecodeReader) Close() error { return nil }
