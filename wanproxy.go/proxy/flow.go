package proxy

import (
	"context"
	"sync"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

const relayQueueLimit = 16 << 20

type delivery struct {
	data      []byte
	responses []byte // ADVANCE/EOS_ACK, sent after delivering data
	eof       bool
	done      bool
}

// flowQueue lets the socket reader process control traffic while the
// application writer is stalled. Small reads coalesce so fragmentation
// cannot turn a bounded byte window into millions of queued objects.
type flowQueue struct {
	mu      sync.Mutex
	items   []delivery
	bytes   int
	closed  bool
	changed chan struct{}
	chunk   bool
}

func newFlowQueue(chunk bool) *flowQueue {
	return &flowQueue{changed: make(chan struct{}), chunk: chunk}
}

func (q *flowQueue) push(msg delivery) error {
	if len(msg.data) == 0 && len(msg.responses) == 0 && !msg.eof && !msg.done {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	size := len(msg.data) + len(msg.responses)
	if q.closed || size > relayQueueLimit-q.bytes {
		return xcodec.ErrSessionLimit
	}
	q.bytes += size
	for len(msg.data) > 0 {
		limit := relayReadSize
		if !q.chunk {
			// A control frame must remain one atomic wire write; encoded
			// data must never be interleaved into the middle of LEARN.
			limit = max(limit, len(msg.data))
		}
		last := len(q.items) - 1
		if last < 0 || q.items[last].eof || q.items[last].done || len(q.items[last].data) >= limit {
			q.items = append(q.items, delivery{data: make([]byte, 0, limit)})
			last++
		}
		n := min(len(msg.data), limit-len(q.items[last].data))
		// Control messages cannot be split across separate queue entries.
		if !q.chunk && n < len(msg.data) {
			q.items = append(q.items, delivery{data: make([]byte, 0, len(msg.data))})
			last++
			n = len(msg.data)
		}
		q.items[last].data = append(q.items[last].data, msg.data[:n]...)
		msg.data = msg.data[n:]
	}
	if len(msg.responses) > 0 || msg.eof || msg.done {
		if len(q.items) == 0 {
			q.items = append(q.items, delivery{})
		}
		last := &q.items[len(q.items)-1]
		last.responses = append(last.responses, msg.responses...)
		last.eof = last.eof || msg.eof
		last.done = last.done || msg.done
	}
	close(q.changed)
	q.changed = make(chan struct{})
	return nil
}

// Plain sources carry no crossed control traffic, so a full queue can
// apply ordinary read backpressure instead of failing the connection.
func (q *flowQueue) pushContext(ctx context.Context, msg delivery) error {
	for {
		q.mu.Lock()
		changed, closed := q.changed, q.closed
		q.mu.Unlock()
		if closed || len(msg.data)+len(msg.responses) > relayQueueLimit {
			return xcodec.ErrSessionLimit
		}
		if err := q.push(msg); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (q *flowQueue) pop(ctx context.Context) (delivery, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			msg := q.items[0]
			q.items[0] = delivery{}
			q.items = q.items[1:]
			q.bytes -= len(msg.data) + len(msg.responses)
			if !q.closed {
				close(q.changed)
				q.changed = make(chan struct{})
			}
			q.mu.Unlock()
			return msg, true
		}
		closed, changed := q.closed, q.changed
		q.mu.Unlock()
		if closed {
			return delivery{}, false
		}
		select {
		case <-ctx.Done():
			return delivery{}, false
		case <-changed:
		}
	}
}

func (q *flowQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.changed)
	}
}

// ADVANCE releases the peer's sending window. Delay it until the matching
// plaintext was forwarded, so queued plaintext stays within that window.
// ASK/LEARN remain independent of application backpressure.
func splitResponses(p []byte) (immediate, delayed []byte) {
	for len(p) > 0 {
		frame, n, _, _ := xcodec.ReadPipeFrame(p) // Session generated these frames.
		if frame.Kind == xcodec.PipeFrameAdvance || frame.Kind == xcodec.PipeFrameEOSAck {
			delayed = append(delayed, p[:n]...)
		} else {
			immediate = append(immediate, p[:n]...)
		}
		p = p[n:]
	}
	return immediate, delayed
}
