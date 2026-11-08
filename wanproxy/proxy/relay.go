package proxy

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/hangtiancheng/wanproxy/wanproxy/pipeline"
	"github.com/hangtiancheng/wanproxy/wanproxy/xcodec"
)

const relayReadSize = 32 * 1024

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

	sessionI *xcodec.Session
	sessionP *xcodec.Session

	clientWire   *wireWriter
	upstreamWire *wireWriter

	log *slog.Logger

	closeOnce sync.Once
	ctx       context.Context
	cancel    context.CancelCauseFunc
}

func NewRelay(client, upstream StreamConn, ifaceCodec, peerCodec *pipeline.CodecSpec, log *slog.Logger) (*Relay, error) {
	if err := ifaceCodec.Validate(); err != nil {
		return nil, err
	}
	if err := peerCodec.Validate(); err != nil {
		return nil, err
	}
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

func (r *Relay) Run() { _ = r.RunContext(context.Background()) }

func (r *Relay) RunContext(ctx context.Context) error {
	r.ctx, r.cancel = context.WithCancelCause(ctx)
	defer r.cancel(nil)
	stop := context.AfterFunc(r.ctx, r.close)
	defer stop()
	var workers sync.WaitGroup
	if r.ifaceCodec == nil && r.peerCodec == nil {
		workers.Go(func() { r.copyPlain(r.upstream, r.client) })
		workers.Go(func() { r.copyPlain(r.client, r.upstream) })
		workers.Wait()
	} else {
		controlsI, controlsP := newFlowQueue(false), newFlowQueue(false)
		var writers sync.WaitGroup
		writers.Go(func() { r.writeControls(controlsI, r.clientWire) })
		writers.Go(func() { r.writeControls(controlsP, r.upstreamWire) })
		toUpstream, toClient := newFlowQueue(true), newFlowQueue(true)
		workers.Go(func() {
			r.readSource(r.client, r.ifaceCodec, r.sessionI, controlsI, toUpstream, countIncomingToCodec(r.ifaceCodec))
		})
		workers.Go(func() {
			r.readSource(r.upstream, r.peerCodec, r.sessionP, controlsP, toClient, countOutgoingToCodec(r.peerCodec))
		})
		workers.Go(func() { r.forward(toUpstream, r.sessionP, r.upstreamWire, controlsI, r.countBoundarySend) })
		workers.Go(func() { r.forward(toClient, r.sessionI, r.clientWire, controlsP, r.countBoundaryReceive) })
		workers.Wait()
		controlsI.close()
		controlsP.close()
		writers.Wait()
	}
	r.close()
	return context.Cause(r.ctx)
}

func (r *Relay) close() {
	r.closeOnce.Do(func() {
		r.client.Close()
		r.upstream.Close()
	})
}

func (r *Relay) abort(err error) {
	r.log.Debug("relay aborted", "error", err)
	r.cancel(err)
	r.close()
}

func (r *Relay) copyPlain(dst, src StreamConn) {
	if _, err := io.Copy(dst, src); err != nil {
		r.abort(err)
		return
	}
	if err := closeWrite(dst); err != nil {
		r.abort(err)
	}
}

func (r *Relay) readSource(conn StreamConn, spec *pipeline.CodecSpec, session *xcodec.Session,
	controls, output *flowQueue, count func(int)) {
	defer output.close()
	src, cleanup, err := r.relaySource(conn, spec, count)
	if err != nil {
		r.abort(err)
		return
	}
	defer cleanup()
	buf := make([]byte, relayReadSize)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			msg := delivery{data: buf[:n]}
			if session != nil {
				res, err := session.Receive(buf[:n])
				if err != nil {
					r.abort(err)
					return
				}
				immediate, delayed := splitResponses(res.Responses)
				if err := controls.push(delivery{data: immediate}); err != nil {
					r.abort(err)
					return
				}
				msg = delivery{data: res.Output, responses: delayed, eof: res.OutputEOF, done: res.Done}
			}
			var err error
			if session == nil {
				err = output.pushContext(r.ctx, msg)
			} else {
				err = output.push(msg)
			}
			if err != nil {
				r.abort(err)
				return
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				if session != nil {
					readErr = session.FinishReceive()
				} else {
					readErr = output.push(delivery{eof: true})
				}
			}
			if readErr != nil {
				r.abort(readErr)
			}
			return
		}
	}
}

func (r *Relay) forward(input *flowQueue, encode *xcodec.Session, wire *wireWriter,
	controls *flowQueue, count func(int)) {
	for {
		msg, ok := input.pop(r.ctx)
		if !ok {
			return
		}
		count(len(msg.data))
		if len(msg.data) > 0 {
			data := msg.data
			if encode != nil {
				var err error
				data, err = encode.EncodeDataContext(r.ctx, data)
				if err != nil {
					r.abort(err)
					return
				}
			}
			if err := wire.Write(data); err != nil {
				r.abort(err)
				return
			}
		}
		if msg.eof {
			var err error
			if encode != nil {
				var eos []byte
				eos, err = encode.CloseWrite()
				if err == nil {
					err = wire.Write(eos)
				}
			} else {
				err = wire.Close()
			}
			if err != nil {
				r.abort(err)
				return
			}
		}
		if err := controls.push(delivery{data: msg.responses, done: msg.done}); err != nil {
			r.abort(err)
			return
		}
	}
}

func (r *Relay) writeControls(input *flowQueue, wire *wireWriter) {
	for {
		msg, ok := input.pop(r.ctx)
		if !ok {
			return
		}
		if err := wire.Write(msg.data); err != nil {
			r.abort(err)
			return
		}
		if msg.done {
			if err := wire.Close(); err != nil {
				r.abort(err)
				return
			}
		}
	}
}

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
