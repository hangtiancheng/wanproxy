package ssh

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

func generateTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

// handshakePair connects a client and server Transport over loopback
// TCP and runs both handshakes concurrently.
func handshakePair(t *testing.T, tagged bool) (*Transport, *Transport) {
	t.Helper()
	hostKey := generateTestKey(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	type result struct {
		t   *Transport
		err error
	}
	serverCh := make(chan result, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverCh <- result{nil, err}
			return
		}
		tr := NewTransport(conn, Config{Role: ServerRole, HostKey: hostKey, Tagged: tagged})
		serverCh <- result{tr, tr.Handshake()}
	}()

	clientConn, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	client := NewTransport(clientConn, Config{Role: ClientRole, Tagged: tagged})
	clientConn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := client.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}

	res := <-serverCh
	if res.err != nil {
		t.Fatalf("server handshake: %v", res.err)
	}
	return client, res.t
}

func TestHandshakeAndDataTagged(t *testing.T) {
	client, server := handshakePair(t, true)
	defer client.Close()
	defer server.Close()

	if client.kex.name() != "diffie-hellman-group-exchange-sha256" {
		t.Fatalf("kex = %s", client.kex.name())
	}
	if client.encC2S.name() != "aes128-ctr" {
		t.Fatalf("enc c2s = %s", client.encC2S.name())
	}
	if client.macC2S.name() != "hmac-sha1" {
		t.Fatalf("mac c2s = %s", client.macC2S.name())
	}
	if !bytes.Equal(client.sessionID, client.exchangeHash) {
		t.Fatalf("session id must be the first exchange hash")
	}
	if !bytes.Equal(client.sessionID, server.sessionID) {
		t.Fatalf("session ids differ between peers")
	}
	if client.sendSeq == 0 || client.recvActivated == false {
		t.Fatalf("client crypto not activated")
	}

	payload := bytes.Repeat([]byte("ssh tagged payload! "), 5000) // 100KB
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := client.Write(payload); err != nil {
			t.Errorf("client write: %v", err)
		}
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatalf("server read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("tagged payload mismatch")
	}

	// Reverse direction.
	go func() {
		defer wg.Done()
		if _, err := server.Write(payload); err != nil {
			t.Errorf("server write: %v", err)
		}
	}()
	got2 := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got2); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if !bytes.Equal(got2, payload) {
		t.Fatalf("reverse payload mismatch")
	}
	wg.Wait()
}

func TestHandshakeAndDataUntagged(t *testing.T) {
	client, server := handshakePair(t, false)
	defer client.Close()
	defer server.Close()

	// Untagged mode carries [u32 len][data] framing internally, like
	// the C++ SSHStream: the write side consumes frames and sends each
	// frame's data as a packet; the read side re-frames each received
	// packet.  End to end the framing is transparent.
	payload := []byte("untagged stream payload")
	var framed []byte
	framed = appendUint32(framed, uint32(len(payload)))
	framed = append(framed, payload...)

	go func() {
		if _, err := client.Write(framed); err != nil {
			t.Errorf("client write: %v", err)
		}
	}()

	got := make([]byte, len(framed))
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatalf("server read: %v", err)
	}
	if !bytes.Equal(got, framed) {
		t.Fatalf("untagged framing = % x, want % x", got, framed)
	}

	// Multiple frames in one write are packetized separately and
	// re-framed identically.
	var two []byte
	two = appendUint32(two, 5)
	two = append(two, "hello"...)
	two = appendUint32(two, 5)
	two = append(two, "world"...)
	go func() {
		if _, err := client.Write(two); err != nil {
			t.Errorf("client write: %v", err)
		}
	}()
	got2 := make([]byte, len(two))
	if _, err := io.ReadFull(server, got2); err != nil {
		t.Fatalf("server read 2: %v", err)
	}
	if !bytes.Equal(got2, two) {
		t.Fatalf("two-frame passthrough = % x, want % x", got2, two)
	}
}

func TestHandshakeRejectsMissingHostKey(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			conn.Close()
		}
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tr := NewTransport(conn, Config{Role: ServerRole})
	if err := tr.Handshake(); err == nil {
		t.Fatalf("server without host key should fail")
	}
}

func TestProtocolPrimitives(t *testing.T) {
	// mpint roundtrip including the high-bit padding case.
	for _, v := range []*big.Int{
		big.NewInt(0), big.NewInt(1), big.NewInt(127), big.NewInt(128),
		big.NewInt(0x7fffffff), new(big.Int).SetBytes(bytes.Repeat([]byte{0xff}, 32)),
	} {
		var encoded []byte
		encoded = appendMPInt(encoded, v)
		got, rest, err := readMPInt(encoded)
		if err != nil {
			t.Fatalf("mpint %v: %v", v, err)
		}
		if got.Cmp(v) != 0 || len(rest) != 0 {
			t.Fatalf("mpint roundtrip: got %v want %v", got, v)
		}
	}

	// The 0x80 case must carry a leading zero byte.
	var encoded []byte
	encoded = appendMPInt(encoded, big.NewInt(0x80))
	if !bytes.Equal(encoded, []byte{0, 0, 0, 2, 0x00, 0x80}) {
		t.Fatalf("mpint(0x80) = % x", encoded)
	}

	// string and namelist
	var s []byte
	s = appendString(s, []byte("hello"))
	got, rest, err := readString(s)
	if err != nil || string(got) != "hello" || len(rest) != 0 {
		t.Fatalf("string roundtrip failed")
	}

	var nl []byte
	nl = appendNameList(nl, []string{"a", "b", "c"})
	names, rest, err := readNameList(nl)
	if err != nil || len(names) != 3 || names[1] != "b" || len(rest) != 0 {
		t.Fatalf("namelist roundtrip failed: %v", names)
	}
}

func TestHostKeyPEMAndSignVerify(t *testing.T) {
	priv := generateTestKey(t)
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})

	loaded, err := LoadRSAPrivateKeyPEM(pemBytes)
	if err != nil {
		t.Fatalf("load pem: %v", err)
	}
	if loaded.N.Cmp(priv.N) != 0 {
		t.Fatalf("loaded key differs")
	}

	server := serverHostKey(loaded)
	client := clientHostKey()

	blob := server.encodePublicKey()
	if err := client.decodePublicKey(blob); err != nil {
		t.Fatalf("decode public key: %v", err)
	}

	message := []byte("exchange hash material")
	sig, err := server.sign(message)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := client.verify(sig, message); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := client.verify(sig, []byte("tampered")); err == nil {
		t.Fatalf("verify should fail for tampered data")
	}
}

func TestDeriveKeyMatchesRFCPattern(t *testing.T) {
	// Self-consistency: the same inputs derive the same key, and the
	// extension loop produces the requested length.
	shared := []byte{0x00, 0x01, 0x02, 0x03}
	h := []byte("exchange-hash-value")
	sid := []byte("session-id")
	k1 := deriveKey(newSHA256, shared, h, sid, 'C', 16)
	k2 := deriveKey(newSHA256, shared, h, sid, 'C', 16)
	if !bytes.Equal(k1, k2) || len(k1) != 16 {
		t.Fatalf("derive mismatch")
	}
	k3 := deriveKey(newSHA256, shared, h, sid, 'D', 16)
	if bytes.Equal(k1, k3) {
		t.Fatalf("different letters must derive different keys")
	}
	long := deriveKey(newSHA1, shared, h, sid, 'C', 64)
	if len(long) != 64 {
		t.Fatalf("long key = %d bytes", len(long))
	}
	// The extension must chain: first 20 bytes = HASH(K||H||x||sid).
	first := newSHA1()
	first.Write(shared)
	first.Write(h)
	first.Write([]byte{'C'})
	first.Write(sid)
	if !bytes.Equal(long[:20], first.Sum(nil)) {
		t.Fatalf("key extension does not chain correctly")
	}
}
