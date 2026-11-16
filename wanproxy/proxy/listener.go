package proxy

import (
	"context"
	"crypto/rsa"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/hangtiancheng/wanproxy/wanproxy/pipeline"
	wanproxyssh "github.com/hangtiancheng/wanproxy/wanproxy/ssh"
)

type SSHSettings struct {
	HostKey                *rsa.PrivateKey
	PeerHostKeyFingerprint string

	HandshakeTimeout time.Duration
}

type Listener struct {
	Name           string
	Iface          Endpoint
	Peer           Endpoint
	IfaceCodec     *pipeline.CodecSpec
	PeerCodec      *pipeline.CodecSpec
	SSH            *SSHSettings
	Log            *slog.Logger
	MaxConnections int
	DialContext    func(context.Context, string, string) (net.Conn, error)

	listenerState
}

func (l *Listener) Start() error {
	if l.SSH != nil {
		if err := wanproxyssh.ValidateHostKey(l.SSH.HostKey); err != nil {
			return err
		}
		if err := wanproxyssh.ValidateHostKeyFingerprint(l.SSH.PeerHostKeyFingerprint); err != nil {
			return err
		}
		if l.SSH.HandshakeTimeout < 0 {
			return fmt.Errorf("proxy %q: negative handshake timeout", l.Name)
		}
	}
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
	return l.start(l.Iface, proxyLogger(l.Log), l.Name, l.MaxConnections, l.handle)
}

func (l *Listener) handle(client net.Conn) {
	defer l.drop(client)

	ctx := l.ctx
	var deadline time.Time
	if l.SSH != nil {
		timeout := l.SSH.HandshakeTimeout
		if timeout == 0 {
			timeout = 15 * time.Second
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
		deadline, _ = ctx.Deadline()
		if err := client.SetDeadline(deadline); err != nil {
			return
		}
	}
	upstream, err := dialPeer(ctx, l.DialContext, l.Peer.Network(), l.Peer.Addr())
	if err != nil {
		proxyLogger(l.Log).Error("could not connect to peer", "proxy", l.Name, "error", err)
		client.Close()
		return
	}
	if !l.track(upstream) {
		return
	}
	defer l.drop(upstream)

	var clientStream, upstreamStream StreamConn = client, upstream
	if l.SSH != nil {
		clientStream, upstreamStream, err = l.handshakeSSH(client, upstream, deadline)
		if err != nil {
			proxyLogger(l.Log).Error("ssh handshake failed", "proxy", l.Name, "error", err)
			client.Close()
			upstream.Close()
			return
		}
	}

	relay, err := NewRelay(clientStream, upstreamStream, l.IfaceCodec, l.PeerCodec, l.Log)
	if err != nil {
		proxyLogger(l.Log).Error("could not build relay", "proxy", l.Name, "error", err)
		client.Close()
		upstream.Close()
		return
	}
	relay.Run()
}

func (l *Listener) handshakeSSH(client, upstream net.Conn, deadline time.Time) (StreamConn, StreamConn, error) {
	if err := client.SetDeadline(deadline); err != nil {
		return nil, nil, err
	}
	if err := upstream.SetDeadline(deadline); err != nil {
		return nil, nil, err
	}

	serverTransport := wanproxyssh.NewTransport(client, wanproxyssh.Config{
		Role:    wanproxyssh.ServerRole,
		HostKey: l.SSH.HostKey,
		Log:     l.Log,
	})
	clientTransport := wanproxyssh.NewTransport(upstream, wanproxyssh.Config{
		Role:                   wanproxyssh.ClientRole,
		PeerHostKeyFingerprint: l.SSH.PeerHostKeyFingerprint,
		Log:                    l.Log,
	})
	succeeded := false
	defer func() {
		if !succeeded {
			serverTransport.Close()
			clientTransport.Close()
		}
	}()

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
	succeeded = true
	return serverTransport, clientTransport, nil
}
