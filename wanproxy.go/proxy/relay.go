package proxy

import (
	"io"
	"log/slog"
	"sync"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

const relayReadSize = 32 * 1024

// Relay moves data between a client (interface side) and an upstream
// (peer side) connection through the codec pipeline composed from the
// interface and peer codec specs, mirroring the C++ ProxyListener /
// ProxyConnector splice arrangement:
//
//	client → upstream: [inflate, session decode] then [session encode, deflate]
//	upstream → client: [inflate, session decode] then [session encode, deflate]
//
// where the first bracket applies when an interface codec is configured
// and the second when a peer codec is configured.  Protocol responses
// cross over between the directions within each session, and half-close
// is propagated with <EOS>/<EOS_ACK> and TCP CloseWrite.
//
// Each direction runs in three phases, like a C++ splice: pump data
// until the logical end of the source stream, finish the opposite write
// side (<EOS> or flush + CloseWrite), then drain the source socket until
// TCP EOF so that crossed control frames are processed and the final
// close can not turn into a reset that loses delivered data.
// StreamConn is the byte-stream interface the relay needs: a net.Conn
// or an SSH transport.
type StreamConn interface {
	io.Reader
	io.Writer
	io.Closer
}

type Relay struct {
	client   StreamConn
	upstream StreamConn

	ifaceCodec *pipeline.CodecSpec
	peerCodec  *pipeline.CodecSpec

	sessionI *xcodec.Session // interface codec session, nil when absent
	sessionP *xcodec.Session // peer codec session, nil when absent

	clientWire   *wireWriter
	upstreamWire *wireWriter

	log *slog.Logger

	closeOnce sync.Once
}

// NewRelay builds a relay for one proxied connection.
func NewRelay(client, upstream StreamConn, ifaceCodec, peerCodec *pipeline.CodecSpec, log *slog.Logger) (*Relay, error) {
	if log == nil {
		log = slog.Default()
	}
	r := &Relay{
		client:     client,
		upstream:   upstream,
		ifaceCodec: ifaceCodec,
		peerCodec:  peerCodec,
		log:        log,
	}

	if ifaceCodec.NeedsSession() {
		r.sessionI = xcodec.NewSession(ifaceCodec.Cache)
	}
	if peerCodec.NeedsSession() {
		r.sessionP = xcodec.NewSession(peerCodec.Cache)
	}

	var err error
	r.clientWire, err = newWireWriter(client, ifaceCodec.NeedsCompressor(), compressorLevelOf(ifaceCodec), countCodecToIncoming(ifaceCodec))
	if err != nil {
		return nil, err
	}
	r.upstreamWire, err = newWireWriter(upstream, peerCodec.NeedsCompressor(), compressorLevelOf(peerCodec), countCodecToOutgoing(peerCodec))
	if err != nil {
		return nil, err
	}
	return r, nil
}

func compressorLevelOf(spec *pipeline.CodecSpec) int {
	if spec == nil {
		return -1
	}
	return spec.CompressorLevel
}

// Run relays until both directions finish, then closes both connections.
func (r *Relay) Run() {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r.clientToUpstream()
	}()
	go func() {
		defer wg.Done()
		r.upstreamToClient()
	}()
	wg.Wait()
	r.client.Close()
	r.upstream.Close()
}

// abort tears down the whole relay, mirroring the C++ splice error path.
func (r *Relay) abort(err error) {
	r.log.Debug("relay aborted", "error", err)
	r.closeOnce.Do(func() {
		r.client.Close()
		r.upstream.Close()
	})
}

// clientToUpstream pumps the interface→peer direction.
func (r *Relay) clientToUpstream() {
	src, cleanup, err := r.relaySource(r.client, r.ifaceCodec, countIncomingToCodec(r.ifaceCodec))
	if err != nil {
		r.abort(err)
		return
	}
	defer cleanup()

	buf := make([]byte, relayReadSize)
	sessionEOF := false
	failed := false
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			data := buf[:n]

			if r.sessionI != nil {
				res, serr := r.sessionI.Receive(data)
				if serr != nil {
					r.abort(serr)
					failed = true
					break
				}
				if len(res.Responses) > 0 {
					if werr := r.clientWire.Write(res.Responses); werr != nil {
						r.abort(werr)
						failed = true
						break
					}
				}
				if res.Done {
					// Both directions of the interface session
					// finished; close the wire toward the client.
					_ = r.clientWire.Close()
				}
				data = res.Output
				if res.OutputEOF {
					sessionEOF = true
				}
			}

			r.countBoundarySend(len(data))
			if len(data) > 0 && !r.forwardToUpstream(data) {
				failed = true
				break
			}
			if sessionEOF {
				break
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			r.abort(rerr)
			failed = true
			break
		}
	}
	if failed {
		return
	}

	// The client closed without ending the session stream: an error
	// only when data is still outstanding, exactly like the C++
	// decoder_consume empty-buffer path.
	if r.sessionI != nil && !sessionEOF && r.sessionI.HasPending() {
		r.abort(io.ErrUnexpectedEOF)
		return
	}

	r.finishTowardUpstream()
	r.drain(src, r.sessionI, r.clientWire)
}

// upstreamToClient pumps the peer→interface direction.
func (r *Relay) upstreamToClient() {
	src, cleanup, err := r.relaySource(r.upstream, r.peerCodec, countOutgoingToCodec(r.peerCodec))
	if err != nil {
		r.abort(err)
		return
	}
	defer cleanup()

	buf := make([]byte, relayReadSize)
	sessionEOF := false
	failed := false
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			data := buf[:n]

			if r.sessionP != nil {
				res, serr := r.sessionP.Receive(data)
				if serr != nil {
					r.abort(serr)
					failed = true
					break
				}
				if len(res.Responses) > 0 {
					if werr := r.upstreamWire.Write(res.Responses); werr != nil {
						r.abort(werr)
						failed = true
						break
					}
				}
				if res.Done {
					// Both directions of the peer session
					// finished; close the wire toward upstream.
					_ = r.upstreamWire.Close()
				}
				data = res.Output
				if res.OutputEOF {
					sessionEOF = true
				}
			}

			r.countBoundaryReceive(len(data))
			if len(data) > 0 && !r.forwardToClient(data) {
				failed = true
				break
			}
			if sessionEOF {
				break
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			r.abort(rerr)
			failed = true
			break
		}
	}
	if failed {
		return
	}

	if r.sessionP != nil && !sessionEOF && r.sessionP.HasPending() {
		r.abort(io.ErrUnexpectedEOF)
		return
	}

	r.finishTowardClient()
	r.drain(src, r.sessionP, r.upstreamWire)
}

// forwardToUpstream sends plaintext toward the peer, encoding through
// the peer session when present.
func (r *Relay) forwardToUpstream(data []byte) bool {
	if r.sessionP != nil {
		out, err := r.sessionP.EncodeData(data)
		if err != nil {
			r.abort(err)
			return false
		}
		if err := r.upstreamWire.Write(out); err != nil {
			r.abort(err)
			return false
		}
		return true
	}
	if err := r.upstreamWire.Write(data); err != nil {
		r.abort(err)
		return false
	}
	return true
}

// forwardToClient sends plaintext toward the client, encoding through
// the interface session when present.
func (r *Relay) forwardToClient(data []byte) bool {
	if r.sessionI != nil {
		out, err := r.sessionI.EncodeData(data)
		if err != nil {
			r.abort(err)
			return false
		}
		if err := r.clientWire.Write(out); err != nil {
			r.abort(err)
			return false
		}
		return true
	}
	if err := r.clientWire.Write(data); err != nil {
		r.abort(err)
		return false
	}
	return true
}

// drain keeps consuming the source after the logical end of stream so
// that crossed control frames (<EOS_ACK>, <ADVANCE>) are processed, the
// session can complete and close its wire, and the socket is fully
// drained before the relay closes it.
func (r *Relay) drain(src io.Reader, session *xcodec.Session, wire *wireWriter) {
	if session == nil {
		return
	}
	buf := make([]byte, relayReadSize)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			res, rerr := session.Receive(buf[:n])
			if rerr != nil {
				r.log.Debug("drain session error", "error", rerr)
				return
			}
			if len(res.Responses) > 0 {
				if werr := wire.Write(res.Responses); werr != nil {
					r.log.Debug("drain response write failed", "error", werr)
				}
			}
			if res.Done {
				_ = wire.Close()
			}
		}
		if err != nil {
			return
		}
	}
}

// finishTowardUpstream ends the client→upstream direction: send <EOS>
// through the peer session when present, otherwise flush and half-close
// the upstream write side.
func (r *Relay) finishTowardUpstream() {
	if r.sessionP != nil {
		eos, err := r.sessionP.CloseWrite()
		if err != nil {
			r.log.Debug("peer session EOS already sent", "error", err)
			return
		}
		if werr := r.upstreamWire.Write(eos); werr != nil {
			r.log.Debug("could not send peer session EOS", "error", werr)
		}
		return
	}
	_ = r.upstreamWire.Close()
}

// finishTowardClient ends the upstream→client direction.
func (r *Relay) finishTowardClient() {
	if r.sessionI != nil {
		eos, err := r.sessionI.CloseWrite()
		if err != nil {
			r.log.Debug("interface session EOS already sent", "error", err)
			return
		}
		if werr := r.clientWire.Write(eos); werr != nil {
			r.log.Debug("could not send interface session EOS", "error", werr)
		}
		return
	}
	_ = r.clientWire.Close()
}

// relaySource wraps a connection for reading, counting raw bytes and
// inflating when the codec compresses the wire stream.
func (r *Relay) relaySource(conn StreamConn, spec *pipeline.CodecSpec, count func(int)) (io.Reader, func(), error) {
	var src io.Reader = conn
	if count != nil {
		src = &countingReader{r: src, count: count}
	}
	if !spec.NeedsCompressor() {
		return src, func() {}, nil
	}
	zr, err := pipeline.NewInflateReader(src)
	if err != nil {
		return nil, nil, err
	}
	return zr, func() { zr.Close() }, nil
}

func countCodecToIncoming(spec *pipeline.CodecSpec) func(int) {
	if spec == nil || !spec.TrackStatistics || spec.Stats == nil {
		return nil
	}
	return func(n int) { spec.Stats.CodecToIncoming.Add(int64(n)) }
}

func countCodecToOutgoing(spec *pipeline.CodecSpec) func(int) {
	if spec == nil || !spec.TrackStatistics || spec.Stats == nil {
		return nil
	}
	return func(n int) { spec.Stats.CodecToOutgoing.Add(int64(n)) }
}

func countIncomingToCodec(spec *pipeline.CodecSpec) func(int) {
	if spec == nil || !spec.TrackStatistics || spec.Stats == nil {
		return nil
	}
	return func(n int) { spec.Stats.IncomingToCodec.Add(int64(n)) }
}

func countOutgoingToCodec(spec *pipeline.CodecSpec) func(int) {
	if spec == nil || !spec.TrackStatistics || spec.Stats == nil {
		return nil
	}
	return func(n int) { spec.Stats.OutgoingToCodec.Add(int64(n)) }
}

// countBoundarySend counts plaintext bytes crossing the interface→peer
// boundary in the client→upstream direction: the C++ chain places
// codec_to_outgoing on the interface side of that boundary and
// incoming_to_codec on the peer side.
func (r *Relay) countBoundarySend(n int) {
	if n == 0 {
		return
	}
	if r.ifaceCodec != nil && r.ifaceCodec.TrackStatistics && r.ifaceCodec.Stats != nil {
		r.ifaceCodec.Stats.CodecToOutgoing.Add(int64(n))
	}
	if r.peerCodec != nil && r.peerCodec.TrackStatistics && r.peerCodec.Stats != nil {
		r.peerCodec.Stats.IncomingToCodec.Add(int64(n))
	}
}

// countBoundaryReceive counts plaintext bytes crossing the peer→interface
// boundary in the upstream→client direction: the C++ chain places
// codec_to_incoming on the peer side of that boundary and
// outgoing_to_codec on the interface side.
func (r *Relay) countBoundaryReceive(n int) {
	if n == 0 {
		return
	}
	if r.peerCodec != nil && r.peerCodec.TrackStatistics && r.peerCodec.Stats != nil {
		r.peerCodec.Stats.CodecToIncoming.Add(int64(n))
	}
	if r.ifaceCodec != nil && r.ifaceCodec.TrackStatistics && r.ifaceCodec.Stats != nil {
		r.ifaceCodec.Stats.OutgoingToCodec.Add(int64(n))
	}
}
