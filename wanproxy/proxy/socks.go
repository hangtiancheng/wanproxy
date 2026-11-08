package proxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"syscall"
	"time"
)

type SocksListener struct {
	Name             string
	Iface            Endpoint
	Log              *slog.Logger
	DialContext      func(context.Context, string, string) (net.Conn, error)
	Dial             func(network, addr string) (net.Conn, error)
	HandshakeTimeout time.Duration

	listenerState
}

func (l *SocksListener) Start() error {
	if !l.Iface.Valid() {
		return fmt.Errorf("socks proxy %q: interface endpoint is incomplete", l.Name)
	}
	if l.Log == nil {
		l.Log = slog.Default()
	}
	return l.start(l.Iface, l.Log, l.Name, l.handle)
}

func (l *SocksListener) handle(client net.Conn) {
	defer l.drop(client)

	timeout := l.HandshakeTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	if err := client.SetDeadline(time.Now().Add(timeout)); err != nil {
		return
	}
	request, err := readSocksConnectRequest(client)
	if err != nil {
		l.Log.Debug("socks handshake failed", "proxy", l.Name, "error", err)
		client.Close()
		return
	}

	network, addr := request.networkAddr()
	upstream, err := dialPeer(l.ctx, l.DialContext, l.Dial, network, addr)
	if err != nil {
		l.Log.Debug("could not connect to socks target", "proxy", l.Name, "target", addr, "error", err)
		_ = writeFull(client, request.failureResponse(err))
		return
	}
	if !l.track(upstream) {
		return
	}
	defer l.drop(upstream)
	if err := writeFull(client, request.successResponse(upstream.LocalAddr())); err != nil {
		return
	}
	if err := client.SetDeadline(time.Time{}); err != nil {
		return
	}

	relay, err := NewRelay(client, upstream, nil, nil, l.Log)
	if err != nil {
		client.Close()
		upstream.Close()
		return
	}
	if len(request.buffered) > 0 {
		if err := writeFull(upstream, request.buffered); err != nil {
			client.Close()
			upstream.Close()
			return
		}
	}
	relay.Run()
}

type socksConnectRequest struct {
	version  int
	host     string
	port     uint16
	addrType byte
	rawAddr  []byte
	buffered []byte
}

func (r *socksConnectRequest) networkAddr() (string, string) {
	if r.addrType == 0x03 {
		return "tcp", net.JoinHostPort(r.host, fmt.Sprintf("%d", r.port))
	}
	if len(r.rawAddr) == 16 {
		ip := net.IP(r.rawAddr)
		return "tcp6", net.JoinHostPort(ip.String(), fmt.Sprintf("%d", r.port))
	}
	return "tcp4", net.JoinHostPort(r.host, fmt.Sprintf("%d", r.port))
}

func readSocksConnectRequest(conn net.Conn) (*socksConnectRequest, error) {
	br := bufio.NewReader(conn)
	version, err := br.ReadByte()
	if err != nil {
		return nil, err
	}
	switch version {
	case 0x04, 0x05:
		var req *socksConnectRequest
		if version == 0x04 {
			req, err = readSocks4Request(br)
		} else {
			req, err = readSocks5Request(conn, br)
		}
		if err != nil {
			return nil, err
		}
		if n := br.Buffered(); n > 0 {
			req.buffered = make([]byte, n)
			if _, err := io.ReadFull(br, req.buffered); err != nil {
				return nil, err
			}
		}
		return req, nil
	default:
		return nil, fmt.Errorf("unsupported SOCKS version %d", version)
	}
}

func readSocks4Request(br *bufio.Reader) (*socksConnectRequest, error) {
	command, err := br.ReadByte()
	if err != nil {
		return nil, err
	}
	if command != 0x01 {
		return nil, fmt.Errorf("unsupported SOCKS4 command %d", command)
	}

	var portBuf [2]byte
	if _, err := io.ReadFull(br, portBuf[:]); err != nil {
		return nil, err
	}
	port := binary.BigEndian.Uint16(portBuf[:])

	addr := make([]byte, 4)
	if _, err := io.ReadFull(br, addr); err != nil {
		return nil, err
	}

	for length := 0; ; length++ {
		if length > 1024 {
			return nil, errors.New("SOCKS4 user id too long")
		}
		b, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == 0x00 {
			break
		}
	}

	host := fmt.Sprintf("%d.%d.%d.%d", addr[0], addr[1], addr[2], addr[3])
	return &socksConnectRequest{
		version:  4,
		host:     host,
		port:     port,
		addrType: 0x01,
		rawAddr:  addr,
	}, nil
}

func readSocks5Request(conn net.Conn, br *bufio.Reader) (*socksConnectRequest, error) {
	methodCount, err := br.ReadByte()
	if err != nil {
		return nil, err
	}
	if methodCount < 1 {
		return nil, errors.New("invalid SOCKS5 method count")
	}
	methods := make([]byte, methodCount)
	if _, err := io.ReadFull(br, methods); err != nil {
		return nil, err
	}
	noAuth := false
	for _, m := range methods {
		if m == 0x00 {
			noAuth = true
		}
	}
	if !noAuth {
		_ = writeFull(conn, []byte{0x05, 0xff})
		return nil, errors.New("SOCKS5 no-auth method is required")
	}
	if err := writeFull(conn, []byte{0x05, 0x00}); err != nil {
		return nil, err
	}

	var header [4]byte
	if _, err := io.ReadFull(br, header[:]); err != nil {
		return nil, err
	}
	if header[0] != 0x05 || header[1] != 0x01 || header[2] != 0x00 {
		return nil, fmt.Errorf("unsupported SOCKS5 request header % x", header[:])
	}

	req := &socksConnectRequest{version: 5, addrType: header[3]}
	switch header[3] {
	case 0x01:
		addr := make([]byte, 4)
		if _, err := io.ReadFull(br, addr); err != nil {
			return nil, err
		}
		req.rawAddr = addr
		req.host = fmt.Sprintf("%d.%d.%d.%d", addr[0], addr[1], addr[2], addr[3])
	case 0x03:
		length, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		if length < 1 {
			return nil, errors.New("invalid SOCKS5 domain length")
		}
		name := make([]byte, length)
		if _, err := io.ReadFull(br, name); err != nil {
			return nil, err
		}
		req.host = string(name)
	case 0x04:
		addr := make([]byte, 16)
		if _, err := io.ReadFull(br, addr); err != nil {
			return nil, err
		}
		req.rawAddr = addr
		req.host = net.IP(addr).String()
	default:
		return nil, fmt.Errorf("unsupported SOCKS5 address type %d", header[3])
	}

	var portBuf [2]byte
	if _, err := io.ReadFull(br, portBuf[:]); err != nil {
		return nil, err
	}
	req.port = binary.BigEndian.Uint16(portBuf[:])

	return req, nil
}

func (r *socksConnectRequest) successResponse(addr net.Addr) []byte {
	if r.version == 4 {
		return []byte{0, 0x5a, 0, 0, 0, 0, 0, 0}
	}
	ip := net.IPv4zero.To4()
	port := 0
	if tcp, ok := addr.(*net.TCPAddr); ok {
		ip, port = tcp.IP, tcp.Port
	}
	response := []byte{5, 0, 0, 1}
	if ipv4 := ip.To4(); ipv4 != nil {
		response = append(response, ipv4...)
	} else {
		response[3] = 4
		response = append(response, ip.To16()...)
	}
	return binary.BigEndian.AppendUint16(response, uint16(port))
}

func (r *socksConnectRequest) failureResponse(err error) []byte {
	if r.version == 4 {
		return []byte{0, 0x5b, 0, 0, 0, 0, 0, 0}
	}
	code := byte(1)
	var dns *net.DNSError
	var neterr net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		code = 5
	case errors.Is(err, syscall.ENETUNREACH):
		code = 3
	case errors.Is(err, syscall.EHOSTUNREACH), errors.As(err, &dns):
		code = 4
	case errors.As(err, &neterr) && neterr.Timeout():
		code = 6
	}
	return []byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0}
}
