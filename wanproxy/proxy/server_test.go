package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestStopCancelsPendingDial(t *testing.T) {
	started := make(chan struct{})
	l := &Listener{
		Iface: Endpoint{Host: "127.0.0.1", Port: "0"},
		Peer:  Endpoint{Host: "127.0.0.1", Port: "9"}, Log: testLogger(),
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Stop() })
	conn, err := net.Dial("tcp", net.JoinHostPort(l.Address().Address, itoa(l.Address().Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("dial did not start")
	}
	done := make(chan error, 1)
	go func() { done <- l.Stop() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not cancel dial")
	}
	if l.ActiveSockets() != 0 {
		t.Fatal("Stop left active connections")
	}
	if err := l.Stop(); err != nil {
		t.Fatalf("repeated Stop: %v", err)
	}
}

func TestStopRejectsLateDialConnection(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	started := make(chan struct{})
	l := &Listener{
		Iface: Endpoint{Host: "127.0.0.1", Port: "0"},
		Peer:  Endpoint{Host: "127.0.0.1", Port: "9"}, Log: testLogger(),
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			close(started)
			<-ctx.Done()
			return a, nil
		},
	}
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", net.JoinHostPort(l.Address().Address, itoa(l.Address().Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	<-started
	if err := l.Stop(); err != nil {
		t.Fatal(err)
	}
	if l.ActiveSockets() != 0 {
		t.Fatal("late connection remained registered")
	}
	b.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := b.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("late connection not closed: %v", err)
	}
}

func TestFleetRollsBackFailedStart(t *testing.T) {
	f := NewFleet(testLogger())
	l := &Listener{Iface: Endpoint{Host: "127.0.0.1", Port: "0"}, Peer: Endpoint{Host: "127.0.0.1", Port: "9"}}
	f.AddTCP(l)
	f.AddSocks(&SocksListener{})
	if err := f.Start(); err == nil {
		t.Fatal("expected startup failure")
	}
	if l.Listening() {
		t.Fatal("failed fleet left listener running")
	}
	if err := f.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestRelayContextCancellation(t *testing.T) {
	client, clientPeer := net.Pipe()
	upstream, upstreamPeer := net.Pipe()
	defer clientPeer.Close()
	defer upstreamPeer.Close()
	r, err := NewRelay(client, upstream, nil, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.RunContext(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("relay error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not stop")
	}
}

func TestStreamConnectionLimitAndReclamation(t *testing.T) {
	var state listenerState
	entered := make(chan net.Conn, 2)
	if err := state.start(Endpoint{Host: "127.0.0.1", Port: "0"}, testLogger(), "limited", 1, func(c net.Conn) {
		defer state.drop(c)
		entered <- c
		_, _ = c.Read(make([]byte, 1))
	}); err != nil {
		t.Fatal(err)
	}
	defer state.Stop()
	dial := func() net.Conn {
		t.Helper()
		addr := state.Address()
		c, err := net.Dial("tcp", net.JoinHostPort(addr.Address, itoa(addr.Port)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	first := dial()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first client not admitted")
	}
	second := dial()
	second.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := second.Read(make([]byte, 1)); err == nil {
		t.Fatal("over-limit connection admitted")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("over-limit connection was left open")
	}
	first.Close()
	eventuallyUDP(t, func() bool { state.mu.Lock(); defer state.mu.Unlock(); return state.activeClients == 0 })
	_ = dial()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("connection capacity not reclaimed")
	}
}

func TestUnexpectedStreamListenerCloseCleansUp(t *testing.T) {
	var state listenerState
	if err := state.start(Endpoint{Host: "127.0.0.1", Port: "0"}, testLogger(), "closed", 1, func(c net.Conn) { state.drop(c) }); err != nil {
		t.Fatal(err)
	}
	state.listener.Close()
	eventuallyUDP(t, func() bool { return !state.Listening() })
	if state.Address() != nil {
		t.Fatal("closed listener retained address")
	}
	if err := state.Stop(); err != nil {
		t.Fatal(err)
	}
}
