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

type SSHSettings struct {
	HostKey *rsa.PrivateKey

	HandshakeTimeout time.Duration
}

type Listener struct {
	Name        string
	Iface       Endpoint
	Peer        Endpoint
	IfaceCodec  *pipeline.CodecSpec
	PeerCodec   *pipeline.CodecSpec
	SSH         *SSHSettings
	Log         *slog.Logger
	DialContext func(context.Context, string, string) (net.Conn, error)
	Dial        func(network, addr string) (net.Conn, error)

	listenerState
}

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
