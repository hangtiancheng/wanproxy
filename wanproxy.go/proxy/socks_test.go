package proxy

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func startSocksProxy(t *testing.T) (*SocksListener, string) {
	t.Helper()
	l := &SocksListener{
		Name:  "socks",
		Iface: Endpoint{Family: FamilyIP, Host: "127.0.0.1", Port: "0"},
		Log:   testLogger(),
	}
	if err := l.Start(); err != nil {
		t.Fatalf("start socks: %v", err)
	}
	addr := l.Address()
	return l, net.JoinHostPort(addr.Address, itoa(addr.Port))
}

func TestSocks5ConnectIPv4(t *testing.T) {
	echo, stopEcho := startEchoServer(t)
	defer stopEcho()
	l, addr := startSocksProxy(t)
	defer l.Stop()

	echoHost, echoPort, _ := net.SplitHostPort(echo)
	ip := net.ParseIP(echoHost).To4()
	port, _ := parsePort(echoPort)

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial socks: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	// Greeting: one method, no auth.
	conn.Write([]byte{0x05, 0x01, 0x00})
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	if !bytes.Equal(greeting, []byte{0x05, 0x00}) {
		t.Fatalf("greeting = % x", greeting)
	}

	// CONNECT to the echo server by IPv4.
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, ip...)
	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], uint16(port))
	req = append(req, portBuf[:]...)
	conn.Write(req)

	// Success response echoes the address, C++ style.
	want := append([]byte{0x05, 0x00, 0x00, 0x01}, ip...)
	want = append(want, portBuf[:]...)
	resp := make([]byte, len(want))
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("connect response: %v", err)
	}
	if !bytes.Equal(resp, want) {
		t.Fatalf("connect response = % x, want % x", resp, want)
	}

	payload := testPayload(8192)
	go func() {
		conn.Write(payload)
		closeWrite(conn)
	}()
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("socks5 echo mismatch: %d bytes", len(got))
	}
}

func TestSocks5ConnectDomain(t *testing.T) {
	echo, stopEcho := startEchoServer(t)
	defer stopEcho()
	l, addr := startSocksProxy(t)
	defer l.Stop()

	echoHost, echoPort, _ := net.SplitHostPort(echo)
	port, _ := parsePort(echoPort)

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial socks: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	conn.Write([]byte{0x05, 0x02, 0x01, 0x00}) // prefer no-auth among two
	greeting := make([]byte, 2)
	io.ReadFull(conn, greeting)
	if !bytes.Equal(greeting, []byte{0x05, 0x00}) {
		t.Fatalf("greeting = % x", greeting)
	}

	name := []byte("localhost")
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(name))}
	req = append(req, name...)
	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], uint16(port))
	req = append(req, portBuf[:]...)
	// Pipeline payload right after the request; it must be forwarded.
	payload := []byte("pipelined hello")
	req = append(req, payload...)
	conn.Write(req)

	want := append([]byte{0x05, 0x00, 0x00, 0x03, byte(len(name))}, name...)
	want = append(want, portBuf[:]...)
	resp := make([]byte, len(want))
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("connect response: %v", err)
	}
	if !bytes.Equal(resp, want) {
		t.Fatalf("connect response = % x, want % x", resp, want)
	}
	_ = echoHost

	go func() {
		closeWrite(conn)
	}()
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("pipelined payload mismatch: %q", got)
	}
}

func TestSocks4Connect(t *testing.T) {
	echo, stopEcho := startEchoServer(t)
	defer stopEcho()
	l, addr := startSocksProxy(t)
	defer l.Stop()

	echoHost, echoPort, _ := net.SplitHostPort(echo)
	ip := net.ParseIP(echoHost).To4()
	port, _ := parsePort(echoPort)

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial socks: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], uint16(port))
	req := []byte{0x04, 0x01, portBuf[0], portBuf[1]}
	req = append(req, ip...)
	req = append(req, []byte("user")...)
	req = append(req, 0x00)
	conn.Write(req)

	want := []byte{0x00, 0x5a, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	resp := make([]byte, 8)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("connect response: %v", err)
	}
	if !bytes.Equal(resp, want) {
		t.Fatalf("socks4 response = % x, want % x", resp, want)
	}

	payload := []byte("socks4 echo payload")
	go func() {
		conn.Write(payload)
		closeWrite(conn)
	}()
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("socks4 echo mismatch: %q", got)
	}
}

func TestSocksRejectsBadHandshakes(t *testing.T) {
	l, addr := startSocksProxy(t)
	defer l.Stop()

	cases := map[string][]byte{
		"bad version":      {0x06, 0x01, 0x00},
		"socks5 no method": {0x05, 0x01, 0x01}, // only GSSAPI offered
		"socks4 bad cmd":   {0x04, 0x02, 0x00, 0x50, 0, 0, 0, 0, 0x00},
	}
	for name, handshake := range cases {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatalf("%s: dial: %v", name, err)
		}
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		conn.Write(handshake)
		// The proxy must close the connection.
		buf := make([]byte, 16)
		n, err := conn.Read(buf)
		if err == nil && n > 0 && name != "socks5 no method" {
			t.Fatalf("%s: expected close, got % x", name, buf[:n])
		}
		conn.Close()
	}
}
