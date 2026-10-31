package proxy

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
)

// SocksListener is a SOCKS4/SOCKS5 CONNECT proxy, mirroring the C++
// ProxySocksListener and ProxySocksConnection: the CONNECT response is
// written as soon as the request is parsed (before the upstream
// connection is established, exactly like the C++ implementation), and
// the connection is then relayed without any codec.
type SocksListener struct {
	Name  string
	Iface Endpoint
	Log   *slog.Logger
	Dial  func(network, addr string) (net.Conn, error) // test hook

	mu                  sync.Mutex
	listener            net.Listener
	acceptedConnections int64
	active              map[net.Conn]struct{}
	stopped             bool
}

// Start begins listening and accepting SOCKS clients.
func (l *SocksListener) Start() error {
	if l.Log == nil {
		l.Log = slog.Default()
	}
	if l.Dial == nil {
		var d net.Dialer
		l.Dial = d.Dial
	}
	if !l.Iface.Valid() {
		return fmt.Errorf("socks proxy %q: interface endpoint is incomplete", l.Name)
	}

	ln, err := net.Listen(l.Iface.Network(), l.Iface.Addr())
	if err != nil {
		return fmt.Errorf("socks proxy %q: %w", l.Name, err)
	}

	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		ln.Close()
		return errors.New("proxy: socks listener already stopped")
	}
	l.listener = ln
	l.active = make(map[net.Conn]struct{})
	l.mu.Unlock()

	go l.acceptLoop()
	return nil
}

func (l *SocksListener) acceptLoop() {
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			l.mu.Lock()
			stopped := l.stopped
			l.mu.Unlock()
			if !stopped {
				l.Log.Error("accept failed", "proxy", l.Name, "error", err)
			}
			return
		}
		l.mu.Lock()
		l.acceptedConnections++
		l.active[conn] = struct{}{}
		l.mu.Unlock()
		go l.handle(conn)
	}
}

func (l *SocksListener) handle(client net.Conn) {
	defer l.drop(client)

	request, err := readSocksConnectRequest(client)
	if err != nil {
		l.Log.Debug("socks handshake failed", "proxy", l.Name, "error", err)
		client.Close()
		return
	}

	// Like the C++ implementation, answer before connecting.
	if _, err := client.Write(request.response); err != nil {
		client.Close()
		return
	}

	network, addr := request.networkAddr()
	upstream, err := l.Dial(network, addr)
	if err != nil {
		l.Log.Error("could not connect to socks target", "proxy", l.Name, "target", addr, "error", err)
		client.Close()
		return
	}
	l.mu.Lock()
	l.active[upstream] = struct{}{}
	l.mu.Unlock()
	defer l.drop(upstream)

	relay, err := NewRelay(client, upstream, nil, nil, l.Log)
	if err != nil {
		client.Close()
		upstream.Close()
		return
	}
	// Replay any data the client sent right after the request.
	if len(request.buffered) > 0 {
		if _, err := upstream.Write(request.buffered); err != nil {
			client.Close()
			upstream.Close()
			return
		}
	}
	relay.Run()
}

func (l *SocksListener) drop(conn net.Conn) {
	l.mu.Lock()
	delete(l.active, conn)
	l.mu.Unlock()
}

// Stop closes the listener and all active connections.
func (l *SocksListener) Stop() error {
	l.mu.Lock()
	l.stopped = true
	ln := l.listener
	conns := make([]net.Conn, 0, len(l.active))
	for conn := range l.active {
		conns = append(conns, conn)
	}
	l.mu.Unlock()

	for _, conn := range conns {
		conn.Close()
	}
	if ln != nil {
		return ln.Close()
	}
	return nil
}

// Listening reports whether the listener is active.
func (l *SocksListener) Listening() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.listener != nil && !l.stopped
}

// Address returns the bound address, if listening.
func (l *SocksListener) Address() *BoundAddress {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return nil
	}
	return boundAddressOf(l.listener)
}

// AcceptedConnections returns the number of accepted client connections.
func (l *SocksListener) AcceptedConnections() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.acceptedConnections
}

// ActiveSockets returns the number of tracked sockets.
func (l *SocksListener) ActiveSockets() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.active)
}

// socksConnectRequest is a parsed SOCKS CONNECT request.
type socksConnectRequest struct {
	version  int
	host     string // dotted IPv4, hex-grouped IPv6, or a domain name
	port     uint16
	addrType byte   // SOCKS5 address type actually supplied
	rawAddr  []byte // raw address bytes (4 or 16), when not a domain
	response []byte // success response to send, C++ style
	buffered []byte // bytes read past the end of the request
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

// readSocksConnectRequest parses a SOCKS4 or SOCKS5 CONNECT request,
// mirroring the C++ ProxySocksConnection state machine.
func readSocksConnectRequest(conn net.Conn) (*socksConnectRequest, error) {
	br := bufio.NewReader(conn)
	version, err := br.ReadByte()
	if err != nil {
		return nil, err
	}
	switch version {
	case 0x04:
		return readSocks4Request(br)
	case 0x05:
		return readSocks5Request(conn, br)
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

	// Consume the null-terminated user id.
	for {
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
		response: []byte{0x00, 0x5a, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
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
		return nil, errors.New("SOCKS5 no-auth method is required")
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
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

	// Build the C++ style success response: it is "fun to lie about
	// what kind of name we were given", echoing a domain as type 0x03
	// and raw address bytes as type 0x01.
	resp := []byte{0x05, 0x00, 0x00}
	if req.addrType == 0x03 {
		resp = append(resp, 0x03, byte(len(req.host)))
		resp = append(resp, req.host...)
	} else {
		resp = append(resp, 0x01)
		resp = append(resp, req.rawAddr...)
	}
	resp = append(resp, portBuf[0], portBuf[1])
	req.response = resp

	// Capture anything the client already sent after the request.
	if n := br.Buffered(); n > 0 {
		buffered := make([]byte, n)
		if _, err := io.ReadFull(br, buffered); err != nil {
			return nil, err
		}
		req.buffered = buffered
	}
	return req, nil
}
