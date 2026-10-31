package proxy

import (
	"context"
	"crypto/rsa"
	"fmt"
	"log/slog"
	"net"
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
	// DialContext overrides outbound dialing. Stop cancels its context.
	DialContext func(context.Context, string, string) (net.Conn, error)
	// Dial is a legacy test hook; it must return before Stop can finish.
	// Prefer DialContext for cancellable dialing.
	Dial func(network, addr string) (net.Conn, error)

	listenerState
}

// Start begins listening and accepting connections.
func (l *Listener) Start() error {
	if err := l.IfaceCodec.Validate(); err != nil {
		return err
	}
	if err := l.PeerCodec.Validate(); err != nil {
		return err
	}
	if !l.Iface.Valid() {
		return fmt.Errorf("proxy %q: interface endpoint is incomplete", l.Name)
	}
	if !l.Peer.Valid() {
		return fmt.Errorf("proxy %q: peer endpoint is incomplete", l.Name)
	}
	if l.Log == nil {
		l.Log = slog.Default()
	}
	return l.start(l.Iface, l.Log, l.Name, l.handle)
}

func (l *Listener) handle(client net.Conn) {
	defer l.drop(client)

	upstream, err := dialPeer(l.ctx, l.DialContext, l.Dial, l.Peer.Network(), l.Peer.Addr())
	if err != nil {
		l.Log.Error("could not connect to peer", "proxy", l.Name, "error", err)
		client.Close()
		return
	}
	if !l.track(upstream) {
		return
	}
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

	type handshakeResult struct {
		side string
		err  error
	}
	results := make(chan handshakeResult, 2)
	go func() { results <- handshakeResult{"interface", serverTransport.Handshake()} }()
	go func() { results <- handshakeResult{"peer", clientTransport.Handshake()} }()
	var firstErr error
	for range 2 {
		res := <-results
		if res.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s side: %w", res.side, res.err)
			// Interrupt the other handshake and join it before returning.
			client.Close()
			upstream.Close()
		}
	}
	if firstErr != nil {
		return nil, nil, firstErr
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
