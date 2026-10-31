package proxy

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
	wanproxyssh "github.com/hangtiancheng/wanproxy/wanproxy.go/ssh"
)

func sshTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

// startSSHEchoServer accepts SSH connections and echoes the framed
// application stream back.
func startSSHEchoServer(t *testing.T, hostKey *rsa.PrivateKey) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				tr := wanproxyssh.NewTransport(conn, wanproxyssh.Config{
					Role:    wanproxyssh.ServerRole,
					HostKey: hostKey,
				})
				conn.SetDeadline(time.Now().Add(20 * time.Second))
				if err := tr.Handshake(); err != nil {
					t.Logf("echo server handshake: %v", err)
					return
				}
				conn.SetDeadline(time.Time{})
				io.Copy(tr, tr)
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// sshClient connects to an SSH server and returns the transport.
func sshClient(t *testing.T, addr string) *wanproxyssh.Transport {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	tr := wanproxyssh.NewTransport(conn, wanproxyssh.Config{Role: wanproxyssh.ClientRole})
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	if err := tr.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	conn.SetDeadline(time.Time{})
	return tr
}

// framedWrite writes [u32 len][payload] through an untagged transport.
func framedWrite(t *testing.T, tr *wanproxyssh.Transport, payload []byte) {
	t.Helper()
	var framed []byte
	framed = appendUint32BE(framed, uint32(len(payload)))
	framed = append(framed, payload...)
	if _, err := tr.Write(framed); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func appendUint32BE(out []byte, v uint32) []byte {
	return append(out, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func framedRead(t *testing.T, tr *wanproxyssh.Transport, wantLen int) []byte {
	t.Helper()
	got := make([]byte, 4+wantLen)
	if _, err := io.ReadFull(tr, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	return got[4:]
}

func TestSSHProxyPassThrough(t *testing.T) {
	hostKey := sshTestKey(t)
	echoAddr := startSSHEchoServer(t, hostKey)

	l := &Listener{
		Name:  "ssh-pass",
		Iface: Endpoint{Family: FamilyIP, Host: "127.0.0.1", Port: "0"},
		Peer:  endpointOf(t, echoAddr),
		SSH:   &SSHSettings{HostKey: hostKey},
		Log:   testLogger(),
	}
	if err := l.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer l.Stop()
	addr := l.Address()

	tr := sshClient(t, net.JoinHostPort(addr.Address, itoa(addr.Port)))
	defer tr.Close()

	payload := bytes.Repeat([]byte("ssh proxy passthrough. "), 4000)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		framedWrite(t, tr, payload)
	}()
	got := framedRead(t, tr, len(payload))
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: %d bytes", len(got))
	}
	wg.Wait()
}

func TestSSHProxyChainWithCodec(t *testing.T) {
	hostKey := sshTestKey(t)
	echoAddr := startSSHEchoServer(t, hostKey)

	// Proxy B: SSH-SSH with the interface codec (WAN side is the
	// interface), plain hop to the echo server.
	cacheB := xcodecCacheForTest()
	specB := &pipeline.CodecSpec{
		Mode:            pipeline.ModeZlibXCodec,
		CompressorLevel: 6,
		Cache:           cacheB,
		TrackStatistics: true,
		Stats:           &pipeline.CodecStats{},
	}
	lb := &Listener{
		Name:       "ssh-server-side",
		Iface:      Endpoint{Family: FamilyIP, Host: "127.0.0.1", Port: "0"},
		Peer:       endpointOf(t, echoAddr),
		IfaceCodec: specB,
		SSH:        &SSHSettings{HostKey: hostKey},
		Log:        testLogger(),
	}
	if err := lb.Start(); err != nil {
		t.Fatalf("start B: %v", err)
	}
	defer lb.Stop()
	addrB := lb.Address()

	// Proxy A: SSH-SSH with the peer codec (WAN side is the peer).
	cacheA := xcodecCacheForTest()
	specA := &pipeline.CodecSpec{
		Mode:            pipeline.ModeZlibXCodec,
		CompressorLevel: 6,
		Cache:           cacheA,
		TrackStatistics: true,
		Stats:           &pipeline.CodecStats{},
	}
	la := &Listener{
		Name:      "ssh-client-side",
		Iface:     Endpoint{Family: FamilyIP, Host: "127.0.0.1", Port: "0"},
		Peer:      Endpoint{Family: FamilyIP, Host: addrB.Address, Port: itoa(addrB.Port)},
		PeerCodec: specA,
		SSH:       &SSHSettings{HostKey: hostKey},
		Log:       testLogger(),
	}
	if err := la.Start(); err != nil {
		t.Fatalf("start A: %v", err)
	}
	defer la.Stop()
	addrA := la.Address()

	tr := sshClient(t, net.JoinHostPort(addrA.Address, itoa(addrA.Port)))
	defer tr.Close()

	payload := bytes.Repeat([]byte("ssh wan-optimized payload! "), 8000)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		framedWrite(t, tr, payload)
	}()
	got := framedRead(t, tr, len(payload))
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %d bytes, want %d", len(got), len(payload))
	}
	wg.Wait()

	// The WAN hop must have deduplicated in both directions: far
	// fewer wire bytes than plaintext crossed between the proxies.
	// The untagged [u32 len] framing crosses the codec chain, as in
	// the C++ implementation, so plaintext counts include 4 bytes.
	framedLen := int64(len(payload)) + 4
	snapA := specA.Stats.Snapshot()
	if snapA.IncomingToCodec != framedLen {
		t.Fatalf("A incoming_to_codec = %d, want %d", snapA.IncomingToCodec, framedLen)
	}
	if snapA.CodecToOutgoing == 0 || snapA.CodecToOutgoing >= int64(len(payload))/10 {
		t.Fatalf("A codec_to_outgoing = %d, want heavy deduplication of %d bytes",
			snapA.CodecToOutgoing, len(payload))
	}
	if snapA.CodecToIncoming != framedLen {
		t.Fatalf("A codec_to_incoming = %d, want %d", snapA.CodecToIncoming, framedLen)
	}
	snapB := specB.Stats.Snapshot()
	if snapB.CodecToOutgoing != framedLen {
		t.Fatalf("B codec_to_outgoing = %d, want %d", snapB.CodecToOutgoing, framedLen)
	}
	if snapB.CodecToIncoming == 0 || snapB.CodecToIncoming >= int64(len(payload))/10 {
		t.Fatalf("B codec_to_incoming = %d, want heavy deduplication of the echo response",
			snapB.CodecToIncoming)
	}
}
