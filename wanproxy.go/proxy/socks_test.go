package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"syscall"
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

	readSocks5Success(t, conn)

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

	readSocks5Success(t, conn)
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

// Read the response by its own address type, independently of the requested
// target. BND.ADDR/BND.PORT describe the proxy's outbound socket.
func readSocks5Success(t *testing.T, conn net.Conn) {
	t.Helper()
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(header[:3], []byte{5, 0, 0}) {
		t.Fatalf("SOCKS5 response: %x", header)
	}
	size := 4
	if header[3] == 4 {
		size = 16
	} else if header[3] != 1 {
		t.Fatalf("unexpected bound address type %d", header[3])
	}
	bound := make([]byte, size+2)
	if _, err := io.ReadFull(conn, bound); err != nil {
		t.Fatal(err)
	}
	if !net.IP(bound[:size]).IsLoopback() || binary.BigEndian.Uint16(bound[size:]) == 0 {
		t.Fatalf("invalid bound endpoint: %x", bound)
	}
}

func TestSocks4PipelinedData(t *testing.T) {
	echo, stopEcho := startEchoServer(t)
	defer stopEcho()
	l, addr := startSocksProxy(t)
	defer l.Stop()
	_, portText, _ := net.SplitHostPort(echo)
	port, _ := parsePort(portText)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	payload := []byte("SOCKS4 buffered application bytes")
	req := []byte{4, 1, byte(port >> 8), byte(port), 127, 0, 0, 1, 0}
	if err := writeFull(conn, append(req, payload...)); err != nil {
		t.Fatal(err)
	}
	var reply [8]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0x5a {
		t.Fatalf("CONNECT rejected: %x", reply)
	}
	closeWrite(conn)
	got, err := io.ReadAll(conn)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("got %q, error %v", got, err)
	}
}

func TestSocksConnectFailure(t *testing.T) {
	l := &SocksListener{
		Iface: Endpoint{Host: "127.0.0.1", Port: "0"}, Log: testLogger(),
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, syscall.ECONNREFUSED
		},
	}
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	defer l.Stop()
	for _, version := range []byte{4, 5} {
		conn, err := net.Dial("tcp", net.JoinHostPort(l.Address().Address, itoa(l.Address().Port)))
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if version == 5 {
			writeFull(conn, []byte{5, 1, 0})
			var greeting [2]byte
			if _, err := io.ReadFull(conn, greeting[:]); err != nil {
				t.Fatal(err)
			}
			writeFull(conn, []byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 9})
			var reply [10]byte
			if _, err := io.ReadFull(conn, reply[:]); err != nil {
				t.Fatal(err)
			}
			if reply[1] != 5 {
				t.Fatalf("SOCKS5 connection refusal = %x", reply)
			}
		} else {
			writeFull(conn, []byte{4, 1, 0, 9, 127, 0, 0, 1, 0})
			var reply [8]byte
			if _, err := io.ReadFull(conn, reply[:]); err != nil {
				t.Fatal(err)
			}
			if reply[1] != 0x5b {
				t.Fatalf("SOCKS4 connection refusal = %x", reply)
			}
		}
		conn.Close()
	}
}

func TestSocks5ReplyUsesIPv6BoundAddressType(t *testing.T) {
	r := &socksConnectRequest{version: 5}
	bound := &net.TCPAddr{IP: net.ParseIP("::1"), Port: 12345}
	got := r.successResponse(bound)
	if len(got) != 22 || !bytes.Equal(got[:4], []byte{5, 0, 0, 4}) ||
		!bytes.Equal(got[4:20], bound.IP.To16()) || binary.BigEndian.Uint16(got[20:]) != uint16(bound.Port) {
		t.Fatalf("invalid IPv6 SOCKS5 reply: %x", got)
	}
}
