package xcodec

import (
	"bytes"
	"testing"
)

func mustEncode(t *testing.T, s *Session, p []byte) []byte {
	t.Helper()
	out, err := s.EncodeData(p)
	if err != nil {
		t.Fatalf("EncodeData: %v", err)
	}
	return out
}

func mustReceive(t *testing.T, s *Session, p []byte) ReceiveResult {
	t.Helper()
	res, err := s.Receive(p)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	return res
}

func TestSessionRoundtripSeparateCaches(t *testing.T) {
	cacheA := NewMemoryCache()
	cacheB := NewMemoryCache()
	a := NewSession(cacheA)
	b := NewSession(cacheB)

	input := deterministicBytes(SegmentLength * 4)

	wire := mustEncode(t, a, input)
	wire = append(wire, mustCloseWrite(t, a)...)

	var output []byte
	var toA []byte
	done := false
	for !done && len(wire) > 0 || !done {
		res := mustReceive(t, b, wire)
		wire = nil
		output = append(output, res.Output...)
		toA = append(toA, res.Responses...)
		if res.OutputEOF && !bytes.Equal(output, input) {
			t.Fatalf("output mismatch at EOF")
		}
		if res.Done {
			done = true
			break
		}

		if len(toA) > 0 {
			resA := mustReceive(t, a, toA)
			toA = nil
			if len(resA.Output) != 0 {
				t.Fatalf("A should not decode data from B here")
			}
			wire = append(wire, resA.Responses...)
			if resA.Done {
				done = true
			}
		}
		if len(wire) == 0 && len(toA) == 0 && !done {
			if !bytes.Equal(output, input) {
				t.Fatalf("exchange stalled with %d/%d bytes", len(output), len(input))
			}
			break
		}
	}

	if !bytes.Equal(output, input) {
		t.Fatalf("roundtrip mismatch: got %d bytes, want %d", len(output), len(input))
	}
}

func mustCloseWrite(t *testing.T, s *Session) []byte {
	t.Helper()
	out, err := s.CloseWrite()
	if err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	return out
}

func TestSessionEncodeReceiveLoopback(t *testing.T) {
	cache := NewMemoryCache()
	s := NewSession(cache)

	input := deterministicBytes(SegmentLength * 2)
	wire := mustEncode(t, s, input)

	res := mustReceive(t, s, wire)
	if !bytes.Equal(res.Output, input) {
		t.Fatalf("loopback output mismatch")
	}
}

func TestSessionRejectsProtocolViolations(t *testing.T) {
	cache := NewMemoryCache()

	cases := []struct {
		name string
		fn   func(s *Session) error
	}{
		{"frame before hello", func(s *Session) error {
			_, err := s.Receive([]byte{PipeOpFrame, 0, 0, 0, 1, 0xaa})
			return err
		}},
		{"ask before hello sent", func(s *Session) error {
			var hello CacheID
			wire := AppendHello(nil, hello)
			wire = AppendAsk(wire, []uint64{1})
			_, err := s.Receive(wire)
			return err
		}},
		{"learn before hello", func(s *Session) error {
			segment := repeatedSegment(1, 1)
			wire := AppendLearn(nil, [][]byte{segment})
			_, err := s.Receive(wire)
			return err
		}},
		{"duplicate hello", func(s *Session) error {
			var hello CacheID
			wire := AppendHello(nil, hello)
			wire = append(wire, AppendHello(nil, hello)...)
			_, err := s.Receive(wire)
			return err
		}},
		{"eos ack before eos", func(s *Session) error {
			var hello CacheID
			wire := AppendHello(nil, hello)
			wire = AppendEOSAck(wire)
			_, err := s.Receive(wire)
			return err
		}},
		{"duplicate eos", func(s *Session) error {
			var hello CacheID
			wire := AppendHello(nil, hello)
			wire = AppendEOS(wire)
			wire = AppendEOS(wire)
			_, err := s.Receive(wire)
			return err
		}},
		{"advance beyond frames", func(s *Session) error {
			mustEncode(t, s, []byte("x"))
			var hello CacheID
			wire := AppendHello(nil, hello)
			wire = AppendAdvance(wire, 5)
			_, err := s.Receive(wire)
			return err
		}},
		{"ask for unknown reference", func(s *Session) error {
			mustEncode(t, s, []byte("x"))
			var hello CacheID
			wire := AppendHello(nil, hello)
			wire = AppendAsk(wire, []uint64{0xdeadbeef})
			_, err := s.Receive(wire)
			return err
		}},
	}

	for _, tc := range cases {
		s := NewSession(cache)
		if err := tc.fn(s); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}
}

func TestSessionGratuitousLearnRejected(t *testing.T) {
	s := NewSession(NewMemoryCache())
	var hello CacheID
	wire := AppendHello(nil, hello)
	segment := repeatedSegment(1, 1)
	wire = AppendLearn(wire, [][]byte{segment})
	if _, err := s.Receive(wire); err == nil {
		t.Fatalf("expected gratuitous LEARN error")
	}
}

func TestSessionHelloConnectsPeerCache(t *testing.T) {
	cache := NewMemoryCache()
	s := NewSession(cache)

	var hello CacheID
	for i := range hello {
		hello[i] = byte(i)
	}
	mustReceive(t, s, AppendHello(nil, hello))

	if got := s.PeerID(); got != hello {
		t.Fatalf("peer id = %x, want %x", got, hello)
	}
	if s.decCache == cache {
		t.Fatalf("decoder cache should be a connected view, not the parent cache")
	}
}

type countingCache struct {
	Cache
	replaces *int
}

func (c countingCache) Replace(hash uint64, segment []byte) {
	*c.replaces++
	c.Cache.Replace(hash, segment)
}

func (c countingCache) Connect(id CacheID) Cache {
	return countingCache{Cache: c.Cache.Connect(id), replaces: c.replaces}
}

func blockedAskExchange(t *testing.T, a *Session, bCache Cache, input []byte) (*Session, []byte) {
	t.Helper()

	firstWire := mustEncode(t, a, input)
	refWire := mustEncode(t, a, input)

	b := NewSession(bCache)
	hello := firstWire[:2+PipeHelloIDLength]
	res := mustReceive(t, b, append(append([]byte{}, hello...), refWire...))
	if len(res.Output) != 0 {
		t.Fatalf("B should block on the unknown reference, got %d bytes", len(res.Output))
	}
	if len(res.Responses) == 0 || res.Responses[0] != PipeOpAsk {
		t.Fatalf("expected an <ASK> from B, got %v", res.Responses)
	}

	resA := mustReceive(t, a, res.Responses)
	if len(resA.Responses) == 0 || resA.Responses[0] != PipeOpLearn {
		t.Fatalf("expected a <LEARN> from A, got %v", resA.Responses)
	}
	return b, resA.Responses
}

func TestSessionRedundantLearnDoesNotReplace(t *testing.T) {
	a := NewSession(NewMemoryCache())

	replaces := 0
	bCache := countingCache{Cache: NewMemoryCache(), replaces: &replaces}

	input := deterministicBytes(SegmentLength)
	b, learn := blockedAskExchange(t, a, bCache, input)

	b.decCache.Enter(HashSegment(input), input)

	res := mustReceive(t, b, learn)
	if !bytes.Equal(res.Output, input) {
		t.Fatalf("output mismatch after redundant <LEARN>: %d bytes", len(res.Output))
	}
	if replaces != 0 {
		t.Fatalf("redundant <LEARN> replaced the cache entry %d times", replaces)
	}
}

func TestSessionLearnNameReuseReplaces(t *testing.T) {
	a := NewSession(NewMemoryCache())

	replaces := 0
	bCache := countingCache{Cache: NewMemoryCache(), replaces: &replaces}

	input := deterministicBytes(SegmentLength)
	b, learn := blockedAskExchange(t, a, bCache, input)

	bogus := deterministicBytes(SegmentLength)
	bogus[0] ^= 0xff
	b.decCache.Enter(HashSegment(input), bogus)

	res := mustReceive(t, b, learn)
	if !bytes.Equal(res.Output, input) {
		t.Fatalf("output mismatch after name-reuse <LEARN>: %d bytes", len(res.Output))
	}
	if replaces != 1 {
		t.Fatalf("name-reuse <LEARN> replaced the cache entry %d times, want 1", replaces)
	}
}
