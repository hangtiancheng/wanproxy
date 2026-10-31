package xcodec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
)

const (
	defaultInFlightBytes = 8 << 20
	maxInFlightFrames    = 256
	maxReceiveBytes      = 16 << 20
)

// ErrBackpressure means the peer must ADVANCE before more data can be encoded.
var ErrBackpressure = errors.New("xcodec: in-flight window is full")

// ErrSessionLimit bounds retained data from a stalled or invalid peer.
var ErrSessionLimit = errors.New("xcodec: session buffer limit exceeded")

type referenceFrame struct {
	refs map[uint64][]byte
	size int
}

// learnedCache pins ASK answers while decoding pending frames. The shared
// cache is an optimization; eviction there must not prevent forward progress.
type learnedCache struct {
	Cache
	segments map[uint64][]byte
}

func (c *learnedCache) Lookup(hash uint64) []byte {
	if seg := c.segments[hash]; seg != nil {
		return seg
	}
	return c.Cache.Lookup(hash)
}

func (c *learnedCache) Has(hash uint64) bool {
	return c.segments[hash] != nil || c.Cache.Has(hash)
}

// Session is one side of an XCodec pipe protocol conversation, mirroring
// the C++ XCodecPipePair.  It is bidirectional: EncodeData/CloseWrite
// produce the wire bytes for the data we send, while Receive consumes
// the wire bytes arriving from the peer, returning decoded plaintext,
// protocol responses to send back, and end-of-stream signals.
//
// The <ASK>/<LEARN>, <ADVANCE> and <EOS>/<EOS_ACK> exchanges cross over
// between the two directions exactly as in the C++ implementation:
// responses produced while decoding the peer's stream are returned to be
// written on the same wire their frames arrived on.
//
// A Session is safe for concurrent use by one encoding goroutine and one
// decoding goroutine.
type Session struct {
	mu    sync.Mutex
	cache Cache
	id    CacheID

	// Encoder half.
	enc               *Encoder
	encHelloSent      bool
	encEOSSent        bool
	encEOSAckReceived bool
	refFrames         []referenceFrame
	inFlightBytes     int
	inFlightLimit     int
	advanced          chan struct{}

	// Decoder half.
	dec            *Decoder
	decCache       Cache
	learned        *learnedCache
	peerID         CacheID
	decBuffer      []byte          // unparsed wire bytes (decoder_buffer_)
	frameBuffer    []byte          // framed encoded bytes (decoder_frame_buffer_)
	frameLengths   []uint64        // decoder_frame_lengths_
	unknown        map[uint64]bool // decoder_unknown_hashes_
	decEOSReceived bool            // decoder_received_eos_
	decEOSAckSent  bool            // encoder_sent_eos_ack_
	decEOSSent     bool            // decoder_sent_eos_
	done           bool            // encoder_produced_eos_
}

// SessionOption customizes a Session.
type SessionOption func(*Session)

// WithSessionID overrides the cache identity sent in <HELLO>.
func WithSessionID(id CacheID) SessionOption {
	return func(s *Session) { s.id = id }
}

// WithInFlightLimit bounds unacknowledged plaintext bytes (default 8 MiB).
// Each EncodeData call must fit within this limit. At most 256 frames may
// be outstanding, including frames without references.
func WithInFlightLimit(n int) SessionOption {
	return func(s *Session) {
		if n > 0 {
			s.inFlightLimit = n
		}
	}
}

// NewSession returns a session backed by the codec cache.  The decode
// side connects a per-peer cache view from the peer's <HELLO> identity,
// mirroring XCodecCache::connect.
//
// Like the C++ XCodecPipePair, the identity announced in <HELLO> is the
// cache's UUID: it is stable across connections, so a peer reconnecting
// to the same cache reuses the decoder cache it connected before and
// references resolve without new <ASK>/<LEARN> exchanges.
func NewSession(cache Cache, opts ...SessionOption) *Session {
	s := &Session{
		cache:         cache,
		unknown:       make(map[uint64]bool),
		inFlightLimit: defaultInFlightBytes,
		advanced:      make(chan struct{}),
	}
	if s.cache == nil {
		s.cache = NewMemoryCache()
	}
	s.id = s.cache.ID()
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ID returns this session's identity.
func (s *Session) ID() CacheID { return s.id }

// PeerID returns the identity the peer announced in <HELLO>, or the zero
// ID before <HELLO> arrives.
func (s *Session) PeerID() CacheID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peerID
}

// EncodeData encodes plaintext data into wire bytes: the <HELLO>
// announcement on first use, then one <FRAME> per PipeMaxPayloadFrame
// chunk.  References made by each frame are retained so that peer <ASK>
// messages can be answered until the peer <ADVANCE>s past the frame.
// ErrBackpressure leaves the input unconsumed; use EncodeDataContext to wait.
func (s *Session) EncodeData(p []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.encodeData(p)
}

// EncodeDataContext waits for the peer to acknowledge outstanding frames,
// without holding the session lock or preventing Receive from answering ASK.
// Calls larger than the configured window return ErrSessionLimit; streaming
// callers should feed chunks no larger than PipeMaxPayloadFrame.
func (s *Session) EncodeDataContext(ctx context.Context, p []byte) ([]byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		out, err := s.encodeData(p)
		advanced := s.advanced
		s.mu.Unlock()
		if !errors.Is(err, ErrBackpressure) {
			return out, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-advanced:
		}
	}
}

func (s *Session) encodeData(p []byte) ([]byte, error) {
	if s.encEOSSent {
		return nil, errors.New("xcodec: encoding after sending EOS")
	}
	frames := len(p) / PipeMaxPayloadFrame
	if len(p)%PipeMaxPayloadFrame != 0 {
		frames++
	}
	if len(p) > s.inFlightLimit || frames > maxInFlightFrames {
		return nil, ErrSessionLimit
	}
	if len(p) > s.inFlightLimit-s.inFlightBytes || frames > maxInFlightFrames-len(s.refFrames) {
		return nil, ErrBackpressure
	}

	var output []byte
	if !s.encHelloSent {
		output = AppendHello(output, s.id)
		s.encHelloSent = true
		s.enc = NewEncoder(s.cache)
	}

	for len(p) > 0 {
		chunk := p
		if len(chunk) > PipeMaxPayloadFrame {
			chunk = chunk[:PipeMaxPayloadFrame]
		}
		p = p[len(chunk):]

		encoded, refs := s.enc.EncodeWithReferences(chunk)
		if len(encoded) == 0 {
			return nil, errors.New("xcodec: encoder produced no output")
		}

		// Track all references associated with this frame, so that we
		// can guarantee we can answer any <ASK> for it until the peer
		// has said they're finished with it.
		s.refFrames = append(s.refFrames, referenceFrame{refs: refs, size: len(chunk)})
		s.inFlightBytes += len(chunk)

		output = AppendPipeFrame(output, encoded)
	}

	return output, nil
}

// CloseWrite produces the <EOS> announcement, indicating that we have no
// intention of sending more data.
func (s *Session) CloseWrite() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.encEOSSent {
		return nil, errors.New("xcodec: EOS has already been sent")
	}

	var output []byte
	if !s.encHelloSent {
		output = AppendHello(output, s.id)
		s.encHelloSent = true
		s.enc = NewEncoder(s.cache)
	}
	output = AppendEOS(output)
	s.encEOSSent = true
	return output, nil
}

// HasPending reports whether received wire data still awaits processing:
// unparsed control bytes, undecoded frame bytes, or a decoder blocked on
// unknown hashes.  The C++ pipe pair uses the same condition to decide
// whether a peer closing the connection lost outstanding data.
func (s *Session) HasPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.decBuffer) > 0 || len(s.frameBuffer) > 0 ||
		(s.dec != nil && s.dec.Buffered() > 0)
}

// FinishReceive validates transport EOF, including the final EOS exchange.
func (s *Session) FinishReceive() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.done || len(s.decBuffer) > 0 || len(s.frameBuffer) > 0 ||
		(s.dec != nil && s.dec.Buffered() > 0) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// ReceiveResult describes the outcome of feeding wire bytes to Receive.
type ReceiveResult struct {
	// Output is decoded plaintext to deliver downstream.
	Output []byte

	// Responses are wire bytes to send back to the peer (<LEARN>,
	// <ADVANCE>, <ASK>, <EOS_ACK>).
	Responses []byte

	// OutputEOF is set once when the peer's <EOS> has been fully
	// processed: the caller should half-close the downstream write
	// side.
	OutputEOF bool

	// Done is set once when we have acknowledged the peer's <EOS> and
	// the peer has acknowledged ours: the session is finished and the
	// wire write side may be closed.
	Done bool
}

// Receive processes wire bytes arriving from the peer, mirroring the C++
// decoder_consume/decoder_decode/decoder_decode_data sequence.
func (s *Session) Receive(p []byte) (ReceiveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result ReceiveResult
	if s.done && len(p) > 0 {
		return result, errors.New("xcodec: data after session completion")
	}
	if len(p) > maxReceiveBytes-len(s.decBuffer) {
		return result, ErrSessionLimit
	}

	s.decBuffer = append(s.decBuffer, p...)

	// Parse as many control frames as we can.
	for len(s.decBuffer) > 0 {
		frame, consumed, status, err := ReadPipeFrame(s.decBuffer)
		if status == PipeFrameReadNeedMore {
			break
		}
		if status == PipeFrameReadInvalid {
			return result, err
		}
		s.decBuffer = s.decBuffer[consumed:]

		if err := s.handleControlFrame(frame, &result); err != nil {
			return result, err
		}
	}

	// Decode any framed data, unless we're still waiting for a <LEARN>.
	if s.dec != nil && len(s.unknown) == 0 && (len(s.frameBuffer) > 0 || s.dec.Buffered() > 0) {
		res := s.dec.Decode(s.frameBuffer)
		s.frameBuffer = nil
		result.Output = append(result.Output, res.Output...)
		if res.Err != nil {
			return result, res.Err
		}

		if err := s.advanceFrames(res.Consumed, &result); err != nil {
			return result, err
		}

		if len(res.Unknown) > 0 {
			for _, hash := range res.Unknown {
				s.unknown[hash] = true
			}
			// Send <ASK>s in groups of PipeAskMax.
			hashes := make([]uint64, 0, len(s.unknown))
			for hash := range s.unknown {
				hashes = append(hashes, hash)
			}
			sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })
			for i := 0; i < len(hashes); i += PipeAskMax {
				end := i + PipeAskMax
				if end > len(hashes) {
					end = len(hashes)
				}
				result.Responses = AppendAsk(result.Responses, hashes[i:end])
			}
		}
		if s.dec.Buffered() == 0 && len(s.unknown) == 0 {
			clear(s.learned.segments)
		}
	}

	// If we have more data to decode, do not fall through to the EOS
	// checks that follow.
	if len(s.decBuffer) > 0 || (s.dec != nil && s.dec.Buffered() > 0) {
		return result, nil
	}

	// We have received EOS and processed everything the peer sent:
	// acknowledge it, and propagate end-of-stream downstream.
	if s.decEOSReceived && !s.decEOSAckSent && len(s.unknown) == 0 {
		result.Responses = AppendEOSAck(result.Responses)
		s.decEOSAckSent = true
	}
	if s.decEOSReceived && !s.decEOSSent && len(s.unknown) == 0 {
		result.OutputEOF = true
		s.decEOSSent = true
	}

	// Both directions finished: the session is done.
	if s.decEOSAckSent && s.encEOSAckReceived && !s.done {
		result.Done = true
		s.done = true
	}

	return result, nil
}

func (s *Session) handleControlFrame(frame PipeFrame, result *ReceiveResult) error {
	switch frame.Kind {
	case PipeFrameHello:
		if s.decCache != nil {
			return errors.New("xcodec: got <HELLO> twice")
		}
		s.peerID = frame.ID
		s.decCache = s.cache.Connect(frame.ID)
		s.learned = &learnedCache{Cache: s.decCache, segments: make(map[uint64][]byte)}
		s.dec = NewDecoder(s.learned)
		s.dec.outputLimit = maxReceiveBytes

	case PipeFrameFrame:
		if s.dec == nil {
			return errors.New("xcodec: got frame data before decoder initialized")
		}
		if s.decEOSReceived {
			return errors.New("xcodec: got frame data after <EOS>")
		}
		if len(s.frameLengths) >= maxInFlightFrames || len(frame.Payload) > maxReceiveBytes-len(s.frameBuffer)-s.dec.Buffered() {
			return ErrSessionLimit
		}
		s.frameLengths = append(s.frameLengths, uint64(len(frame.Payload)))
		s.frameBuffer = append(s.frameBuffer, frame.Payload...)

	case PipeFrameAsk:
		if s.enc == nil {
			return errors.New("xcodec: got <ASK> before sending <HELLO>")
		}
		segments := make([][]byte, 0, len(frame.Hashes))
		for _, hash := range frame.Hashes {
			segment := s.findReference(hash)
			if segment == nil {
				return fmt.Errorf("xcodec: hash in <ASK> could not be found in any reference frame: 0x%016x", hash)
			}
			segments = append(segments, segment)
		}
		result.Responses = AppendLearn(result.Responses, segments)

	case PipeFrameLearn:
		if s.decCache == nil {
			return errors.New("xcodec: got <LEARN> before <HELLO>")
		}
		for _, segment := range frame.Segments {
			hash := HashSegment(segment)
			if !s.unknown[hash] {
				return errors.New("xcodec: gratuitous <LEARN> without <ASK>")
			}
			delete(s.unknown, hash)
			if len(s.learned.segments) >= maxReceiveBytes/SegmentLength {
				return ErrSessionLimit
			}
			s.learned.segments[hash] = cloneSegment(segment)
			if oseg := s.decCache.Lookup(hash); oseg != nil {
				if segmentsEqual(oseg, segment) {
					// Redundant <LEARN>: the cache already
					// holds the same data for the hash;
					// leave it alone, as the C++ pipe pair
					// does.
					continue
				}
				// Name reuse in <LEARN>: replace the old
				// name with this one, as the C++ pipe pair
				// does.
				s.decCache.Replace(hash, segment)
			} else {
				s.decCache.Enter(hash, segment)
			}
		}

	case PipeFrameAdvance:
		if s.enc == nil {
			return errors.New("xcodec: got <ADVANCE> before sending <HELLO>")
		}
		count := int(frame.Count)
		if count > len(s.refFrames) {
			return fmt.Errorf("xcodec: invalid frame advance count %d", count)
		}
		for i := 0; i < count; i++ {
			s.inFlightBytes -= s.refFrames[i].size
			s.refFrames[i] = referenceFrame{}
		}
		s.refFrames = s.refFrames[count:]
		close(s.advanced)
		s.advanced = make(chan struct{})

	case PipeFrameEOS:
		if s.dec == nil {
			return errors.New("xcodec: got <EOS> before <HELLO>")
		}
		if s.decEOSReceived {
			return errors.New("xcodec: duplicate <EOS>")
		}
		s.decEOSReceived = true

	case PipeFrameEOSAck:
		if !s.encEOSSent {
			return errors.New("xcodec: got <EOS_ACK> before sending <EOS>")
		}
		if s.encEOSAckReceived {
			return errors.New("xcodec: duplicate <EOS_ACK>")
		}
		if len(s.refFrames) != 0 {
			return errors.New("xcodec: got <EOS_ACK> with unacknowledged frames")
		}
		s.encEOSAckReceived = true
	}
	return nil
}

// advanceFrames maps consumed frame-buffer bytes onto whole frames and
// emits <ADVANCE> for them, mirroring the C++ bookkeeping.
func (s *Session) advanceFrames(consumed int, result *ReceiveResult) error {
	if consumed < 0 {
		return errors.New("xcodec: negative frame consumption")
	}
	remaining := uint64(consumed)
	var advance uint32
	for len(s.frameLengths) > 0 && remaining > 0 {
		head := s.frameLengths[0]
		if remaining < head {
			s.frameLengths[0] = head - remaining
			remaining = 0
			break
		}
		remaining -= head
		advance++
		s.frameLengths = s.frameLengths[1:]
	}
	if advance > 0 {
		result.Responses = AppendAdvance(result.Responses, advance)
	}
	return nil
}

// findReference searches the retained reference frames for a hash,
// oldest frame first, like the C++ encoder_reference_frames_ walk.
func (s *Session) findReference(hash uint64) []byte {
	for _, refmap := range s.refFrames {
		if segment, ok := refmap.refs[hash]; ok {
			return segment
		}
	}
	return nil
}
