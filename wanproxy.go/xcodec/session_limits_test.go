package xcodec

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// A LEARN batch may be larger than the persistent cache. Its entries must
// remain available until the blocked frames have actually been decoded.
func TestSessionLearnSurvivesCacheEviction(t *testing.T) {
	a := NewSession(NewMemoryCache())
	input := append(testSegment(1), testSegment(2)...)
	first := mustEncode(t, a, input)
	refs := mustEncode(t, a, input)
	b := NewSession(NewMemoryCache(WithSegmentLimit(1)))
	wire := append(bytes.Clone(first[:2+PipeHelloIDLength]), refs...)
	res := mustReceive(t, b, wire)
	learn := mustReceive(t, a, res.Responses).Responses
	res = mustReceive(t, b, learn)
	if !bytes.Equal(res.Output, input) {
		t.Fatalf("LEARN did not unblock decoding: got %d of %d bytes, responses %x", len(res.Output), len(input), res.Responses)
	}
	if b.HasPending() {
		t.Fatal("decoded frames still pending")
	}
}

func TestSessionBackpressureAndCancellation(t *testing.T) {
	a := NewSession(NewMemoryCache(), WithInFlightLimit(SegmentLength))
	b := NewSession(NewMemoryCache())
	wire := mustEncode(t, a, testSegment(1))
	if _, err := a.EncodeData([]byte("blocked")); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("expected backpressure, got %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.EncodeDataContext(ctx, testSegment(2))
		done <- err
	}()
	// Decoding and ADVANCE must remain possible while encoding is blocked.
	mustReceive(t, a, mustReceive(t, b, wire).Responses)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() {
		_, err := a.EncodeDataContext(ctx2, []byte("still blocked"))
		done <- err
	}()
	cancel2()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("encoding did not cancel")
	}
	if _, err := a.EncodeData(make([]byte, SegmentLength+1)); !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("oversized encode: %v", err)
	}
}

func TestSessionBoundsTinyFrames(t *testing.T) {
	s := NewSession(NewMemoryCache())
	for range maxInFlightFrames {
		mustEncode(t, s, []byte("x"))
	}
	if _, err := s.EncodeData([]byte("x")); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("unbounded tiny frame retention: %v", err)
	}
}

func TestSessionRejectsTruncatedTransport(t *testing.T) {
	s := NewSession(NewMemoryCache())
	mustReceive(t, s, AppendHello(nil, CacheID{1}))
	if err := s.FinishReceive(); err == nil {
		t.Fatal("transport ended without EOS")
	}
}

func TestSessionBoundsPendingFrames(t *testing.T) {
	s := NewSession(NewMemoryCache())
	mustReceive(t, s, AppendHello(nil, CacheID{1}))
	ref := []byte{Magic, OpRef, 0, 0, 0, 0, 0, 0, 0, 1}
	for range maxInFlightFrames {
		mustReceive(t, s, AppendPipeFrame(nil, ref))
	}
	if _, err := s.Receive(AppendPipeFrame(nil, ref)); !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("unbounded pending frame retention: %v", err)
	}
}

func TestSessionRejectsDataAfterEOS(t *testing.T) {
	s := NewSession(NewMemoryCache())
	wire := AppendEOS(AppendHello(nil, CacheID{1}))
	mustReceive(t, s, wire)
	if _, err := s.Receive(AppendPipeFrame(nil, []byte("lost data"))); err == nil {
		t.Fatal("accepted data after signaling output EOF")
	}
}

func TestSessionRequiresHelloBeforeEOS(t *testing.T) {
	s := NewSession(NewMemoryCache())
	if _, err := s.Receive(AppendEOS(nil)); err == nil {
		t.Fatal("accepted EOS before HELLO")
	}
}

func TestSessionRetainsOutOfBandDeclarations(t *testing.T) {
	a := NewSession(NewNullCache(CacheID{1}))
	b := NewSession(NewMemoryCache())
	input := testSegment(1)
	res := mustReceive(t, b, mustEncode(t, a, input))
	learn := mustReceive(t, a, res.Responses).Responses
	res = mustReceive(t, b, learn)
	if !bytes.Equal(res.Output, input) {
		t.Fatal("out-of-band ASK answer was not retained")
	}
}
