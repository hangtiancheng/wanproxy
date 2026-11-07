package proxy

import (
	"bytes"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func startEchoServer(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
				closeWrite(c)
			}(conn)
		}
	}()
	return ln.Addr().String(), func() { ln.Close(); <-done }
}

func endpointOf(t *testing.T, addr string) Endpoint {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	return Endpoint{Family: FamilyIP, Host: host, Port: port}
}

func startTCPProxy(t *testing.T, name string, upstream Endpoint, ifaceCodec, peerCodec *pipeline.CodecSpec) (*Listener, Endpoint) {
	t.Helper()
	l := &Listener{
		Name:       name,
		Iface:      Endpoint{Family: FamilyIP, Host: "127.0.0.1", Port: "0"},
		Peer:       upstream,
		IfaceCodec: ifaceCodec,
		PeerCodec:  peerCodec,
		Log:        testLogger(),
	}
	if err := l.Start(); err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	addr := l.Address()
	if addr == nil {
		t.Fatalf("proxy has no bound address")
	}
	return l, Endpoint{Family: FamilyIP, Host: addr.Address, Port: itoa(addr.Port)}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

func roundtrip(t *testing.T, addr Endpoint, payload []byte) []byte {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr.Addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(20 * time.Second))

	go func() {
		conn.Write(payload)
		closeWrite(conn)
	}()

	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return got
}

func testPayload(size int) []byte {
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i/64) ^ byte(i%7)
	}
	return payload
}

func TestRelayPassThrough(t *testing.T) {
	echo, stopEcho := startEchoServer(t)
	defer stopEcho()

	l, addr := startTCPProxy(t, "pass", endpointOf(t, echo), nil, nil)
	defer l.Stop()

	payload := testPayload(100 * 1024)
	if got := roundtrip(t, addr, payload); !bytes.Equal(got, payload) {
		t.Fatalf("passthrough mismatch: got %d bytes", len(got))
	}
	if n := l.AcceptedConnections(); n != 1 {
		t.Fatalf("accepted = %d, want 1", n)
	}
}

func TestRelayZlibIncoming(t *testing.T) {
	echo, stopEcho := startEchoServer(t)
	defer stopEcho()

	spec := &pipeline.CodecSpec{Mode: pipeline.ModeZlib, CompressorLevel: 6}
	l, addr := startTCPProxy(t, "zlib", endpointOf(t, echo), nil, spec)
	defer l.Stop()

	payload := testPayload(64 * 1024)
	if got := roundtrip(t, addr, payload); !bytes.Equal(got, payload) {
		t.Fatalf("zlib roundtrip mismatch: got %d bytes", len(got))
	}
}

func TestRelayXCodecIncomingEcho(t *testing.T) {
	echo, stopEcho := startEchoServer(t)
	defer stopEcho()

	spec := &pipeline.CodecSpec{Mode: pipeline.ModeXCodec, Cache: xcodec.NewMemoryCache()}
	l, addr := startTCPProxy(t, "xcodec", endpointOf(t, echo), nil, spec)
	defer l.Stop()

	payload := testPayload(16 * 1024)
	if got := roundtrip(t, addr, payload); !bytes.Equal(got, payload) {
		t.Fatalf("xcodec echo roundtrip mismatch: got %d bytes", len(got))
	}
}

func TestRelayZlibXCodecIncomingEcho(t *testing.T) {
	echo, stopEcho := startEchoServer(t)
	defer stopEcho()

	spec := &pipeline.CodecSpec{
		Mode:            pipeline.ModeZlibXCodec,
		CompressorLevel: 6,
		Cache:           xcodec.NewMemoryCache(),
	}
	l, addr := startTCPProxy(t, "zlib+xcodec", endpointOf(t, echo), nil, spec)
	defer l.Stop()

	payload := testPayload(32 * 1024)
	if got := roundtrip(t, addr, payload); !bytes.Equal(got, payload) {
		t.Fatalf("zlib+xcodec echo roundtrip mismatch: got %d bytes", len(got))
	}
}

func TestTwoProxyChain(t *testing.T) {
	for _, mode := range []pipeline.Mode{pipeline.ModeXCodec, pipeline.ModeZlibXCodec} {
		t.Run(mode.String(), func(t *testing.T) {
			echo, stopEcho := startEchoServer(t)
			defer stopEcho()

			specB := &pipeline.CodecSpec{
				Mode:            mode,
				CompressorLevel: 6,
				Cache:           xcodec.NewMemoryCache(),
				TrackStatistics: true,
				Stats:           &pipeline.CodecStats{},
			}
			lb, addrB := startTCPProxy(t, "server-side", endpointOf(t, echo), specB, nil)
			defer lb.Stop()

			specA := &pipeline.CodecSpec{
				Mode:            mode,
				CompressorLevel: 6,
				Cache:           xcodec.NewMemoryCache(),
				TrackStatistics: true,
				Stats:           &pipeline.CodecStats{},
			}
			la, addrA := startTCPProxy(t, "client-side", addrB, nil, specA)
			defer la.Stop()

			payload := testPayload(256 * 1024)
			if got := roundtrip(t, addrA, payload); !bytes.Equal(got, payload) {
				t.Fatalf("chain roundtrip mismatch: got %d bytes, want %d", len(got), len(payload))
			}

			snapA := specA.Stats.Snapshot()
			if snapA.IncomingToCodec != int64(len(payload)) {
				t.Fatalf("A incoming_to_codec = %d, want %d", snapA.IncomingToCodec, len(payload))
			}
			if snapA.CodecToOutgoing == 0 || snapA.CodecToOutgoing >= int64(len(payload)) {
				t.Fatalf("A codec_to_outgoing = %d, want wire bytes < %d", snapA.CodecToOutgoing, len(payload))
			}
			if snapA.CodecToIncoming != int64(len(payload)) {
				t.Fatalf("A codec_to_incoming = %d, want %d", snapA.CodecToIncoming, len(payload))
			}

			snapB := specB.Stats.Snapshot()
			if snapB.CodecToOutgoing != int64(len(payload)) {
				t.Fatalf("B codec_to_outgoing = %d, want %d", snapB.CodecToOutgoing, len(payload))
			}
			if snapB.OutgoingToCodec != int64(len(payload)) {
				t.Fatalf("B outgoing_to_codec = %d, want %d", snapB.OutgoingToCodec, len(payload))
			}
			if snapB.CodecToIncoming == 0 || snapB.CodecToIncoming >= int64(len(payload)) {
				t.Fatalf("B codec_to_incoming = %d, want wire bytes < %d", snapB.CodecToIncoming, len(payload))
			}
		})
	}
}

func TestTwoProxyChainRandomData(t *testing.T) {
	echo, stopEcho := startEchoServer(t)
	defer stopEcho()

	specB := &pipeline.CodecSpec{Mode: pipeline.ModeXCodec, Cache: xcodec.NewMemoryCache()}
	lb, addrB := startTCPProxy(t, "server-side", endpointOf(t, echo), specB, nil)
	defer lb.Stop()

	specA := &pipeline.CodecSpec{Mode: pipeline.ModeXCodec, Cache: xcodec.NewMemoryCache()}
	la, addrA := startTCPProxy(t, "client-side", addrB, nil, specA)
	defer la.Stop()

	payload := make([]byte, 64*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if got := roundtrip(t, addrA, payload); !bytes.Equal(got, payload) {
		t.Fatalf("random chain roundtrip mismatch: got %d bytes", len(got))
	}
}

func TestChainDeduplication(t *testing.T) {
	echo, stopEcho := startEchoServer(t)
	defer stopEcho()

	specB := &pipeline.CodecSpec{Mode: pipeline.ModeXCodec, Cache: xcodec.NewMemoryCache(), TrackStatistics: true, Stats: &pipeline.CodecStats{}}
	lb, addrB := startTCPProxy(t, "server-side", endpointOf(t, echo), specB, nil)
	defer lb.Stop()

	specA := &pipeline.CodecSpec{Mode: pipeline.ModeXCodec, Cache: xcodec.NewMemoryCache(), TrackStatistics: true, Stats: &pipeline.CodecStats{}}
	la, addrA := startTCPProxy(t, "client-side", addrB, nil, specA)
	defer la.Stop()

	payload := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 2048)

	if got := roundtrip(t, addrA, payload); !bytes.Equal(got, payload) {
		t.Fatalf("first roundtrip mismatch")
	}
	first := specA.Stats.Snapshot().CodecToOutgoing

	if got := roundtrip(t, addrA, payload); !bytes.Equal(got, payload) {
		t.Fatalf("second roundtrip mismatch")
	}
	second := specA.Stats.Snapshot().CodecToOutgoing - first

	if second >= first {
		t.Fatalf("no deduplication: first=%d second=%d wire bytes", first, second)
	}
	if second > int64(len(payload))/8 {
		t.Fatalf("second transfer moved too many wire bytes: %d of %d", second, len(payload))
	}
}

func xcodecCacheForTest() *xcodec.MemoryCache {
	return xcodec.NewMemoryCache()
}

func TestThreeProxyChainWithSmallCaches(t *testing.T) {
	for _, mode := range []pipeline.Mode{pipeline.ModeXCodec, pipeline.ModeZlibXCodec} {
		t.Run(mode.String(), func(t *testing.T) {
			echo, stopEcho := startEchoServer(t)
			defer stopEcho()
			codec := func() *pipeline.CodecSpec {
				return &pipeline.CodecSpec{Mode: mode, CompressorLevel: 1, Cache: xcodec.NewMemoryCache(xcodec.WithSegmentLimit(3))}
			}
			last, lastAddr := startTCPProxy(t, "last", endpointOf(t, echo), codec(), nil)
			defer last.Stop()
			middle, middleAddr := startTCPProxy(t, "middle", lastAddr, codec(), codec())
			defer middle.Stop()
			first, firstAddr := startTCPProxy(t, "first", middleAddr, nil, codec())
			defer first.Stop()
			payload := testPayload(18 << 20)
			if got := roundtrip(t, firstAddr, payload); !bytes.Equal(got, payload) {
				t.Fatalf("large chain: got %d bytes of %d", len(got), len(payload))
			}
		})
	}
}
