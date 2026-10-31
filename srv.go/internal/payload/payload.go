// Package payload implements the deterministic benchmark payload
// generator used by the SRV's file and benchmark endpoints.  Transfer
// integrity is verified by SHA-256 without exchanging the payload.
//
// LCG: x = (x*1103515245 + 12345) mod 2^31; byte = (x >> 16) & 0xff.
// Note that mod 2^31 of the true product equals (product mod 2^32) &
// 0x7fffffff because 2^32 ≡ 0 (mod 2^31).  Byte-identical output is
// critical: benchmarks compare SHA-256 digests across the generator,
// the /api/bench/source endpoint and the /api/files/generate endpoint.
package payload

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
)

// Pattern selects how the payload bytes are produced.
type Pattern string

const (
	Random Pattern = "random"
	Repeat Pattern = "repeat"
	Mixed  Pattern = "mixed"
	Text   Pattern = "text"
)

// BlockSize is the 4096-byte block granularity used by the repeat and
// mixed patterns.
const BlockSize = 4096

// ParsePattern validates a pattern name.
func ParsePattern(s string) (Pattern, error) {
	switch s {
	case string(Random):
		return Random, nil
	case string(Repeat):
		return Repeat, nil
	case string(Mixed):
		return Mixed, nil
	case string(Text):
		return Text, nil
	}
	return "", fmt.Errorf("unknown pattern %q (want random|repeat|mixed|text)", s)
}

// The text pattern mimics an application log stream: long runs of
// identical, highly compressible vocabulary lines (which XCodec also
// deduplicates once warm) punctuated by unique lines carrying sequence
// numbers, trace tokens and latencies (which no optimizer can remove).
// Every 40-line period holds 30 fixed vocabulary lines (~2.5KB, a
// byte-identical run longer than one 2048-byte XCodec segment)
// followed by 10 unique lines, so roughly three quarters of the bytes
// are exact repeats.
var textVocabulary = []string{
	`level=INFO msg="request completed" method=GET path=/api/v1/users status=200 bytes=1843`,
	`level=INFO msg="cache hit" store=redis key=session.ttl hit=true latency_us=142`,
	`level=INFO msg="db query executed" table=orders rows=37 latency_ms=3 plan=index_scan`,
	`level=WARN msg="retrying upstream call" upstream=payments attempt=2 backoff_ms=250`,
	`level=INFO msg="request completed" method=POST path=/api/v1/orders status=201 bytes=921`,
	`level=INFO msg="health check ok" component=api uptime_s=86413 goroutines=214`,
	`level=INFO msg="static asset served" path=/assets/app.css status=304 etag=W/"9f2a"`,
	`level=ERROR msg="connection reset by peer" remote=10.4.2.18:52413 read_bytes=40960`,
	`level=INFO msg="background job finished" queue=emails job=send_digest duration_ms=812`,
	`level=INFO msg="rate limiter state" client_id=web-frontend remaining=412 window_s=60`,
	`level=DEBUG msg="tls handshake complete" version=TLS1.3 cipher=TLS_AES_128_GCM_SHA256`,
	`level=INFO msg="metrics flushed" series=1204 endpoint=prometheus:9090 duration_ms=44`,
}

var textHandlers = []string{
	"ListUsers", "GetOrder", "CreateOrder", "SearchItems", "UpdateCart",
	"Checkout", "RefreshSession", "UploadAsset",
}

type lcg struct{ x uint32 }

func newLCG(seed int64) *lcg { return &lcg{x: uint32(seed & 0x7fffffff)} }

func (g *lcg) next() byte {
	g.x = (g.x*1103515245 + 12345) & 0x7fffffff
	return byte((g.x >> 16) & 0xff)
}

func (g *lcg) fillBlock(b []byte) {
	for i := range b {
		b[i] = g.next()
	}
}

// Reader streams a deterministic payload of a fixed size.  It
// implements io.Reader and returns io.EOF once size bytes are read.
type Reader struct {
	pattern Pattern
	size    int64
	off     int64
	g       *lcg

	blockA, blockB, blockC []byte
	fresh                  []byte

	textLine []byte
	textPos  int
	textSeq  int64
}

// NewReader builds a payload reader for (pattern, size, seed).
func NewReader(pattern Pattern, size int64, seed int64) *Reader {
	r := &Reader{pattern: pattern, size: size, g: newLCG(seed)}
	switch pattern {
	case Repeat:
		r.blockA = make([]byte, BlockSize)
		r.g.fillBlock(r.blockA)
	case Mixed:
		r.blockA = make([]byte, BlockSize)
		r.blockB = make([]byte, BlockSize)
		r.blockC = make([]byte, BlockSize)
		r.g.fillBlock(r.blockA)
		r.g.fillBlock(r.blockB)
		r.g.fillBlock(r.blockC)
		r.fresh = make([]byte, BlockSize)
	}
	return r
}

// Read fills p with the next payload bytes.
func (r *Reader) Read(p []byte) (int, error) {
	if r.off >= r.size {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && r.off < r.size {
		switch r.pattern {
		case Random:
			p[n] = r.g.next()
			n++
			r.off++
		case Repeat:
			pos := int(r.off % BlockSize)
			c := copy(p[n:], r.blockA[pos:])
			if r.off+int64(c) > r.size {
				c = int(r.size - r.off)
			}
			n += c
			r.off += int64(c)
		case Mixed:
			chunk := r.off / BlockSize
			pos := int(r.off % BlockSize)
			var src []byte
			if m := chunk % 10; m == 3 || m == 7 || m == 9 {
				if pos == 0 {
					r.g.fillBlock(r.fresh)
				}
				src = r.fresh
			} else {
				switch chunk % 3 {
				case 0:
					src = r.blockA
				case 1:
					src = r.blockB
				default:
					src = r.blockC
				}
			}
			c := copy(p[n:], src[pos:])
			if r.off+int64(c) > r.size {
				c = int(r.size - r.off)
			}
			n += c
			r.off += int64(c)
		case Text:
			for n < len(p) && r.off < r.size {
				if r.textPos == len(r.textLine) {
					r.fillTextLine()
				}
				c := copy(p[n:], r.textLine[r.textPos:])
				if r.off+int64(c) > r.size {
					c = int(r.size - r.off)
				}
				r.textPos += c
				n += c
				r.off += int64(c)
			}
		}
	}
	return n, nil
}

// fillTextLine renders the next log line.  Slots 0..29 of every
// 40-line period are vocabulary lines indexed by the slot, so every
// fixed run is byte-identical; slots 30..39 are unique lines carrying
// the global sequence number, an LCG trace token and an LCG latency.
func (r *Reader) fillTextLine() {
	seq := r.textSeq
	r.textSeq++
	r.textPos = 0

	slot := int(seq % 40)
	if slot >= 30 {
		var tok [4]byte
		for i := range tok {
			tok[i] = r.g.next()
		}
		latency := int(r.g.next())*4 + int(r.g.next())
		r.textLine = fmt.Appendf(r.textLine[:0],
			`2026-01-15T10:%02d:%02d.%03dZ seq=%d level=INFO msg="request handled" handler=%s duration_ms=%d trace=%x client=10.%d.%d.%d`+"\n",
			(seq/60)%60, seq%60, seq%1000, seq,
			textHandlers[slot%len(textHandlers)], latency, tok,
			r.g.next(), r.g.next(), r.g.next())
		return
	}
	line := textVocabulary[slot%len(textVocabulary)]
	r.textLine = append(r.textLine[:0], line...)
	r.textLine = append(r.textLine, '\n')
}

// SHA256 regenerates the payload and returns its hex digest.
func SHA256(pattern Pattern, size int64, seed int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, NewReader(pattern, size, seed)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashingReader wraps a reader and computes a hash of everything read.
type HashingReader struct {
	R io.Reader
	H hash.Hash
}

// NewHashingReader wraps r, hashing all bytes read into h.
func NewHashingReader(r io.Reader, h hash.Hash) *HashingReader {
	return &HashingReader{R: r, H: h}
}

func (hr *HashingReader) Read(p []byte) (int, error) {
	n, err := hr.R.Read(p)
	if n > 0 {
		hr.H.Write(p[:n])
	}
	return n, err
}
