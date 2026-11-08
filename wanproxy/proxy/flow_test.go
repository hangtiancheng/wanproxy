package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hangtiancheng/wanproxy/wanproxy/pipeline"
	"github.com/hangtiancheng/wanproxy/wanproxy/xcodec"
)

func TestCodecControlsProgressOverUnbufferedChain(t *testing.T) {
	for _, mode := range []pipeline.Mode{pipeline.ModeXCodec, pipeline.ModeZlibXCodec} {
		t.Run(mode.String(), func(t *testing.T) {
			client, front := net.Pipe()
			wanA, wanB := net.Pipe()
			wanC, wanD := net.Pipe()
			back, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			spec := func() *pipeline.CodecSpec {
				return &pipeline.CodecSpec{Mode: mode, CompressorLevel: 1, Cache: xcodec.NewMemoryCache(xcodec.WithSegmentLimit(3))}
			}
			a, err := NewRelay(front, wanA, nil, spec(), testLogger())
			if err != nil {
				t.Fatal(err)
			}
			b, err := NewRelay(wanB, wanC, spec(), spec(), testLogger())
			if err != nil {
				t.Fatal(err)
			}
			c, err := NewRelay(wanD, back, spec(), nil, testLogger())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 3)
			for _, relay := range []*Relay{a, b, c} {
				go func() { done <- relay.RunContext(ctx) }()
			}
			echoDone := make(chan struct{})
			go func() { defer close(echoDone); io.Copy(server, server) }()
			payload := testPayload(1 << 20)
			writeDone := make(chan error, 1)
			go func() { writeDone <- writeFull(client, payload) }()
			client.SetReadDeadline(time.Now().Add(8 * time.Second))
			got := make([]byte, len(payload))
			_, readErr := io.ReadFull(client, got)
			cancel()
			client.Close()
			server.Close()
			for range 3 {
				<-done
			}
			<-echoDone
			if readErr != nil || !bytes.Equal(got, payload) {
				t.Fatalf("control traffic stalled: %v", readErr)
			}
			if err := <-writeDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFlowQueueCoalescesFragmentedReads(t *testing.T) {
	q := newFlowQueue(true)
	for range relayReadSize * 2 {
		if err := q.push(delivery{data: []byte{1}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(q.items) != 2 {
		t.Fatalf("fragmentation allocated %d queue entries", len(q.items))
	}
	q.close()
	for range 2 {
		msg, ok := q.pop(context.Background())
		if !ok || len(msg.data) != relayReadSize {
			t.Fatal("lost queued data")
		}
	}
}

func TestControlQueueNeverSplitsFrames(t *testing.T) {
	q := newFlowQueue(false)
	first := xcodec.AppendPipeFrame(nil, make([]byte, 28000))
	learn := xcodec.AppendLearn(nil, [][]byte{make([]byte, xcodec.SegmentLength), make([]byte, xcodec.SegmentLength), make([]byte, xcodec.SegmentLength)})
	if err := q.push(delivery{data: first}); err != nil {
		t.Fatal(err)
	}
	if err := q.push(delivery{data: learn}); err != nil {
		t.Fatal(err)
	}
	q.close()
	for {
		msg, ok := q.pop(context.Background())
		if !ok {
			break
		}
		for len(msg.data) > 0 {
			_, n, status, err := xcodec.ReadPipeFrame(msg.data)
			if status != xcodec.PipeFrameReadOK {
				t.Fatalf("control frame split across writes: %v", err)
			}
			msg.data = msg.data[n:]
		}
	}
}

func TestPlainQueueBackpressureCancels(t *testing.T) {
	q := newFlowQueue(true)
	if err := q.push(delivery{data: make([]byte, relayQueueLimit)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.pushContext(ctx, delivery{data: []byte{1}}) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("queue did not cancel")
	}
}
