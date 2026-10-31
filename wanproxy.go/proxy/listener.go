package proxy

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
	wanproxyssh "github.com/hangtiancheng/wanproxy/wanproxy.go/ssh"
)

// SSHSettings enables the SSH-SSH proxy type: the interface hop runs an
// SSH server transport and the peer hop an SSH client transport, like
// the C++ SSHProxyListener/SSHProxyConnector pair.
type SSHSettings struct {
	// HostKey is the RSA server host key for the interface hop.
	HostKey *rsa.PrivateKey

	// HandshakeTimeout bounds each SSH handshake; zero means 15s.
	HandshakeTimeout time.Duration
}

// Listener is a TCP-TCP proxy, mirroring the C++ ProxyListener and
// ProxyConnector pair: it accepts connections on the interface endpoint
// and connects each one to the peer endpoint, relaying through the
// configured interface and peer codecs.
type Listener struct {
	Name       string
	Iface      Endpoint
	Peer       Endpoint
	IfaceCodec *pipeline.CodecSpec
	PeerCodec  *pipeline.CodecSpec
	SSH        *SSHSettings
	Log        *slog.Logger
	Dial       func(network, addr string) (net.Conn, error) // test hook

	mu                  sync.Mutex
	listener            net.Listener
	acceptedConnections int64
	active              map[net.Conn]struct{}
	stopped             bool
}

// Start begins listening and accepting connections.
func (l *Listener) Start() error {
	if l.Log == nil {
		l.Log = slog.Default()
	}
	if l.Dial == nil {
		var d net.Dialer
		l.Dial = d.Dial
	}
	if !l.Iface.Valid() {
		return fmt.Errorf("proxy %q: interface endpoint is incomplete", l.Name)
	}
	if !l.Peer.Valid() {
		return fmt.Errorf("proxy %q: peer endpoint is incomplete", l.Peer)
	}

	ln, err := net.Listen(l.Iface.Network(), l.Iface.Addr())
	if err != nil {
		return fmt.Errorf("proxy %q: %w", l.Name, err)
	}

	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		ln.Close()
		return errors.New("proxy: listener already stopped")
	}
	l.listener = ln
	l.active = make(map[net.Conn]struct{})
	l.mu.Unlock()

	go l.acceptLoop()
	return nil
}

func (l *Listener) acceptLoop() {
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

func (l *Listener) handle(client net.Conn) {
	defer l.drop(client)

	upstream, err := l.Dial(l.Peer.Network(), l.Peer.Addr())
	if err != nil {
		l.Log.Error("could not connect to peer", "proxy", l.Name, "error", err)
		client.Close()
		return
	}
	l.mu.Lock()
	l.active[upstream] = struct{}{}
	l.mu.Unlock()
	defer l.drop(upstream)

	var clientStream, upstreamStream StreamConn = client, upstream
	if l.SSH != nil {
		clientStream, upstreamStream, err = l.handshakeSSH(client, upstream)
		if err != nil {
			l.Log.Error("ssh handshake failed", "proxy", l.Name, "error", err)
			client.Close()
			upstream.Close()
			return
		}
	}

	relay, err := NewRelay(clientStream, upstreamStream, l.IfaceCodec, l.PeerCodec, l.Log)
	if err != nil {
		l.Log.Error("could not build relay", "proxy", l.Name, "error", err)
		client.Close()
		upstream.Close()
		return
	}
	relay.Run()
}

// handshakeSSH runs the interface-side SSH server transport and the
// peer-side SSH client transport concurrently, mirroring the C++
// SSHProxyConnector which starts both SSH streams together.
//
// Packet tagging follows the C++ SSHStream framing rule: a hop is
// 0xff-tagged exactly when the codec on that hop is active.
func (l *Listener) handshakeSSH(client, upstream net.Conn) (StreamConn, StreamConn, error) {
	timeout := l.SSH.HandshakeTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if err := client.SetDeadline(deadline); err != nil {
		return nil, nil, err
	}
	if err := upstream.SetDeadline(deadline); err != nil {
		return nil, nil, err
	}

	serverTransport := wanproxyssh.NewTransport(client, wanproxyssh.Config{
		Role:    wanproxyssh.ServerRole,
		HostKey: l.SSH.HostKey,
		Tagged:  codecActive(l.IfaceCodec),
		Log:     l.Log,
	})
	clientTransport := wanproxyssh.NewTransport(upstream, wanproxyssh.Config{
		Role:   wanproxyssh.ClientRole,
		Tagged: codecActive(l.PeerCodec),
		Log:    l.Log,
	})

	serverErr := make(chan error, 1)
	clientErr := make(chan error, 1)
	go func() { serverErr <- serverTransport.Handshake() }()
	go func() { clientErr <- clientTransport.Handshake() }()

	if err := <-serverErr; err != nil {
		return nil, nil, fmt.Errorf("interface side: %w", err)
	}
	if err := <-clientErr; err != nil {
		return nil, nil, fmt.Errorf("peer side: %w", err)
	}

	var noDeadline time.Time
	if err := client.SetDeadline(noDeadline); err != nil {
		return nil, nil, err
	}
	if err := upstream.SetDeadline(noDeadline); err != nil {
		return nil, nil, err
	}
	return serverTransport, clientTransport, nil
}

func codecActive(spec *pipeline.CodecSpec) bool {
	return spec != nil && spec.Mode != pipeline.ModeNone
}

func (l *Listener) drop(conn net.Conn) {
	l.mu.Lock()
	delete(l.active, conn)
	l.mu.Unlock()
}

// Stop closes the listener and all active connections.
func (l *Listener) Stop() error {
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
func (l *Listener) Listening() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.listener != nil && !l.stopped
}

// Address returns the bound address, if listening.
func (l *Listener) Address() *BoundAddress {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return nil
	}
	return boundAddressOf(l.listener)
}

// AcceptedConnections returns the number of accepted client connections.
func (l *Listener) AcceptedConnections() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.acceptedConnections
}

// ActiveSockets returns the number of tracked sockets.
func (l *Listener) ActiveSockets() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.active)
}
