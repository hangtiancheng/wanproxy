package ssh

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	cryptossh "golang.org/x/crypto/ssh"
)

func generateTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func keyFingerprint(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	fp, err := PublicKeyFingerprint(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func handshakePair(t *testing.T) (*Transport, *Transport) {
	t.Helper()
	key := generateTestKey(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type result struct {
		tr  *Transport
		err error
	}
	serverCh := make(chan result, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverCh <- result{err: err}
			return
		}
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		server := NewTransport(conn, Config{Role: ServerRole, HostKey: key})
		serverCh <- result{server, server.Handshake()}
	}()
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	client := NewTransport(conn, Config{Role: ClientRole, PeerHostKeyFingerprint: keyFingerprint(t, key)})
	t.Cleanup(func() { client.Close() })
	if err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	res := <-serverCh
	if res.err != nil {
		t.Fatal(res.err)
	}
	t.Cleanup(func() { res.tr.Close() })
	return client, res.tr
}

func TestAuthenticatedStreamDuplexAndHalfClose(t *testing.T) {
	client, server := handshakePair(t)
	negotiated := client.sshConn.(cryptossh.AlgorithmsConnMetadata).Algorithms()
	insecure := cryptossh.InsecureAlgorithms()
	if slices.Contains(insecure.KeyExchanges, negotiated.KeyExchange) || slices.Contains(insecure.HostKeys, negotiated.HostKey) ||
		slices.Contains(insecure.Ciphers, negotiated.Read.Cipher) || slices.Contains(insecure.Ciphers, negotiated.Write.Cipher) ||
		slices.Contains(insecure.MACs, negotiated.Read.MAC) || slices.Contains(insecure.MACs, negotiated.Write.MAC) {
		t.Fatalf("negotiated insecure algorithms: %+v", negotiated)
	}
	// Larger than the SSH channel window: both directions must drain
	// concurrently, and EOF must preserve every byte in both directions.
	payload := bytes.Repeat([]byte("authenticated stream\x00\xff"), 200000)
	var wg sync.WaitGroup
	for _, tr := range []*Transport{client, server} {
		wg.Go(func() {
			if n, err := tr.Write(payload); err != nil || n != len(payload) {
				t.Errorf("write: %d %v", n, err)
			}
			if err := tr.CloseWrite(); err != nil {
				t.Errorf("half close: %v", err)
			}
		})
		wg.Go(func() {
			got, err := io.ReadAll(tr)
			if err != nil || !bytes.Equal(got, payload) {
				t.Errorf("read: length=%d error=%v", len(got), err)
			}
		})
	}
	wg.Wait()
}

func TestHandshakeRejectsUntrustedHostKey(t *testing.T) {
	for _, fp := range []string{"", "SHA256:invalid", keyFingerprint(t, generateTestKey(t))} {
		t.Run(fp, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			key := generateTestKey(t)
			done := make(chan error, 1)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					done <- err
					return
				}
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				tr := NewTransport(conn, Config{Role: ServerRole, HostKey: key})
				err = tr.Handshake()
				tr.Close()
				done <- err
			}()
			conn, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			client := NewTransport(conn, Config{Role: ClientRole, PeerHostKeyFingerprint: fp})
			if err := client.Handshake(); err == nil {
				t.Error("accepted untrusted server")
			}
			client.Close()
			if err := <-done; err == nil {
				t.Error("untrusted handshake completed")
			}
		})
	}
}

func TestHandshakeRequiresServerKey(t *testing.T) {
	tr := NewTransport(nil, Config{Role: ServerRole})
	if err := tr.Handshake(); err == nil {
		t.Fatal("accepted missing key/connection")
	}
	if _, err := tr.Write([]byte("data")); err == nil {
		t.Fatal("write after failed handshake")
	}
}

func TestHostKeyPEM(t *testing.T) {
	key := generateTestKey(t)
	data := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	loaded, err := LoadRSAPrivateKeyPEM(data)
	if err != nil || loaded.N.Cmp(key.N) != 0 {
		t.Fatalf("load key: %v", err)
	}
	if _, err := LoadRSAPrivateKeyPEM(append(data, data...)); err == nil {
		t.Fatal("accepted multiple keys")
	}
	if err := ValidateHostKey(&rsa.PrivateKey{}); err == nil {
		t.Fatal("accepted incomplete RSA key")
	}
}

func TestSingleChannelAndRequestRejection(t *testing.T) {
	key := generateTestKey(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	servers := make(chan *Transport, 1)
	errors := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			errors <- err
			return
		}
		c.SetDeadline(time.Now().Add(5 * time.Second))
		tr := NewTransport(c, Config{Role: ServerRole, HostKey: key})
		servers <- tr
		errors <- tr.Handshake()
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	pub, err := cryptossh.NewPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	client, channels, requests, err := cryptossh.NewClientConn(c, ln.Addr().String(), &cryptossh.ClientConfig{User: "wanproxy", HostKeyCallback: cryptossh.FixedHostKey(pub)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	requestsDone, channelsDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(requestsDone); cryptossh.DiscardRequests(requests) }()
	go func() { defer close(channelsDone); rejectChannels(channels) }()
	server := <-servers
	defer func() { client.Close(); server.Close(); <-requestsDone; <-channelsDone }()
	for _, request := range []struct {
		kind string
		data []byte
	}{{"session", nil}, {channelType, []byte{1}}} {
		if ch, _, err := client.OpenChannel(request.kind, request.data); err == nil {
			ch.Close()
			t.Fatal("accepted unsupported channel")
		}
	}
	channel, channelRequests, err := client.OpenChannel(channelType, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	go cryptossh.DiscardRequests(channelRequests)
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	if ch, _, err := client.OpenChannel(channelType, nil); err == nil {
		ch.Close()
		t.Fatal("accepted a second stream")
	}
	if accepted, _, err := client.SendRequest("tcpip-forward", true, nil); err != nil || accepted {
		t.Fatalf("global request: accepted=%v error=%v", accepted, err)
	}
	if accepted, err := channel.SendRequest("exec", true, nil); err != nil || accepted {
		t.Fatalf("channel request: accepted=%v error=%v", accepted, err)
	}
}
