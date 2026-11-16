package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func udpEcho(t *testing.T, network, addr string) Endpoint {
	t.Helper()
	pc, err := net.ListenPacket(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.(*net.UDPConn).SetWriteBuffer(udpMaxDatagramSize); err != nil {
		pc.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, udpMaxDatagramSize)
		for {
			n, peer, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := pc.WriteTo(buf[:n], peer); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { pc.Close(); <-done })
	return endpointOf(t, pc.LocalAddr().String())
}

type udpErrorConn struct {
	net.Conn
	readErr, writeErr   error
	readOnce, writeOnce sync.Once
}

func (c *udpErrorConn) Read(p []byte) (int, error) {
	var err error
	c.readOnce.Do(func() { err = c.readErr })
	if err != nil {
		return 0, &net.OpError{Op: "read", Net: "udp", Err: err}
	}
	return c.Conn.Read(p)
}

func (c *udpErrorConn) Write(p []byte) (int, error) {
	var err error
	c.writeOnce.Do(func() { err = c.writeErr })
	if err != nil {
		return 0, &net.OpError{Op: "write", Net: "udp", Err: err}
	}
	return c.Conn.Write(p)
}

func TestUDPRecoversPeerReadErrors(t *testing.T) {
	for _, fault := range []error{syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ENOBUFS} {
		t.Run(fault.Error(), func(t *testing.T) {
			l := &UDPListener{Iface: Endpoint{Host: "127.0.0.1", Port: "0"}, Peer: udpEcho(t, "udp4", "127.0.0.1:0"),
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
					if err != nil {
						return nil, err
					}
					return &udpErrorConn{Conn: conn, readErr: fault}, nil
				},
			}
			udpExchange(t, udpClient(t, startUDP(t, l)), []byte("after asynchronous UDP error"))
			if st := l.Status(); st.CreatedSessions != 1 || st.ActiveSessions != 1 {
				t.Fatalf("recoverable error destroyed session: %+v", st)
			}
		})
	}
}

func TestUDPRecoversPeerWriteErrors(t *testing.T) {
	for _, fault := range []error{syscall.EMSGSIZE, syscall.ECONNREFUSED, syscall.ENOBUFS, context.DeadlineExceeded} {
		t.Run(fault.Error(), func(t *testing.T) {
			l := &UDPListener{Iface: Endpoint{Host: "127.0.0.1", Port: "0"}, Peer: udpEcho(t, "udp4", "127.0.0.1:0"),
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
					if err != nil {
						return nil, err
					}
					return &udpErrorConn{Conn: conn, writeErr: fault}, nil
				},
			}
			c := udpClient(t, startUDP(t, l))
			if _, err := c.Write([]byte("dropped packet")); err != nil {
				t.Fatal(err)
			}
			eventuallyUDP(t, func() bool { return l.Status().DroppedDatagrams == 1 })
			udpExchange(t, c, []byte("same session after packet error"))
			if st := l.Status(); st.CreatedSessions != 1 || st.ActiveSessions != 1 || st.ClientToPeerDatagrams != 1 {
				t.Fatalf("recoverable error destroyed session: %+v", st)
			}
		})
	}
}

type udpPacketErrorConn struct {
	*net.UDPConn
	readErr, writeErr   error
	readOnce, writeOnce sync.Once
}

func (c *udpPacketErrorConn) ReadFromUDPAddrPort(p []byte) (int, netip.AddrPort, error) {
	var err error
	c.readOnce.Do(func() { err = c.readErr })
	if err != nil {
		return 0, netip.AddrPort{}, &net.OpError{Op: "read", Net: "udp", Err: err}
	}
	return c.UDPConn.ReadFromUDPAddrPort(p)
}

func (c *udpPacketErrorConn) WriteToUDPAddrPort(p []byte, addr netip.AddrPort) (int, error) {
	var err error
	c.writeOnce.Do(func() { err = c.writeErr })
	if err != nil {
		return 0, &net.OpError{Op: "write", Net: "udp", Err: err}
	}
	return c.UDPConn.WriteToUDPAddrPort(p, addr)
}

func TestUDPRecoversListenerErrors(t *testing.T) {
	for _, op := range []string{"read", "write"} {
		t.Run(op, func(t *testing.T) {
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			fault := &udpPacketErrorConn{UDPConn: conn}
			if op == "read" {
				fault.readErr = syscall.ECONNRESET
			} else {
				fault.writeErr = syscall.ENOBUFS
			}
			ctx, cancel := context.WithCancel(context.Background())
			l := &UDPListener{conn: fault, ctx: ctx, cancel: cancel, sessions: make(map[netip.AddrPort]*udpSession),
				Peer: udpEcho(t, "udp4", "127.0.0.1:0"), IdleTimeout: time.Second, MaxSessions: 2, MaxQueuedBytes: DefaultUDPMaxQueuedBytes, Log: testLogger(), writeToken: make(chan struct{}, 1)}
			l.writeToken <- struct{}{}
			l.wg.Add(2)
			go l.receive()
			go l.expire()
			t.Cleanup(func() { l.Stop() })
			c := udpClient(t, conn.LocalAddr().String())
			if op == "write" {
				if _, err := c.Write([]byte("lost reply")); err != nil {
					t.Fatal(err)
				}
				eventuallyUDP(t, func() bool { return l.Status().DroppedDatagrams == 1 })
			}
			udpExchange(t, c, []byte("listener survived packet error"))
			if st := l.Status(); !st.Listening || st.CreatedSessions != 1 || st.ActiveSessions != 1 {
				t.Fatalf("packet error destroyed listener/session: %+v", st)
			}
		})
	}
}

type udpBlockedReplyConn struct {
	udpPacketConn
	writer  net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *udpBlockedReplyConn) SetWriteDeadline(deadline time.Time) error {
	return c.writer.SetWriteDeadline(deadline)
}

func (c *udpBlockedReplyConn) WriteToUDPAddrPort(p []byte, _ netip.AddrPort) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.writer.Write(p)
}

func TestUDPExpiredReplyReleasesSharedWriter(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := &udpBlockedReplyConn{writer: a, started: make(chan struct{})}
	l := &UDPListener{conn: conn, IdleTimeout: time.Hour, writeToken: make(chan struct{}, 1)}
	l.writeToken <- struct{}{}
	s := &udpSession{ctx: ctx}
	done := make(chan error, 1)
	go func() { _, err := l.writeReply(s, []byte("blocked")); done <- err }()
	select {
	case <-conn.started:
	case <-time.After(time.Second):
		t.Fatal("reply did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expired write succeeded")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("session cancellation left the shared reply writer blocked")
	}
	// A cancellation callback must finish before the next session sets its
	// deadline, otherwise it can poison an unrelated client's write.
	go func() { _, err := l.writeReply(&udpSession{ctx: context.Background()}, []byte("healthy")); done <- err }()
	b.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, len("healthy"))
	if _, err := io.ReadFull(b, got); err != nil || string(got) != "healthy" {
		t.Fatalf("next reply: %q %v", got, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func startUDP(t *testing.T, l *UDPListener) string {
	t.Helper()
	l.Log = testLogger()
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Stop(); err != nil {
			t.Error(err)
		}
	})
	a := l.Address()
	return net.JoinHostPort(a.Address, itoa(a.Port))
}

func udpClient(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.(*net.UDPConn).SetWriteBuffer(udpMaxDatagramSize); err != nil {
		t.Fatal(err)
	}
	return c
}

func udpExchange(t *testing.T, c net.Conn, payload []byte) {
	t.Helper()
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("UDP write: %d %v", n, err)
	}
	buf := make([]byte, udpMaxDatagramSize)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("UDP read: %v", err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Fatalf("datagram mismatch: got %d, want %d bytes", n, len(payload))
	}
}

func eventuallyUDP(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("UDP condition did not become true")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUDPDatagramBoundaries(t *testing.T) {
	for _, family := range []struct {
		name, host string
		family     Family
	}{{"IPv4", "127.0.0.1", FamilyIPv4}, {"IPv6", "::1", FamilyIPv6}} {
		t.Run(family.name, func(t *testing.T) {
			if family.family == FamilyIPv6 {
				probe, err := net.ListenPacket("udp6", "[::1]:0")
				if err != nil {
					t.Skipf("IPv6 unavailable: %v", err)
				}
				probe.Close()
			}
			upstream := udpEcho(t, "udp", net.JoinHostPort(family.host, "0"))
			upstream.Family = family.family
			l := &UDPListener{Iface: Endpoint{Family: family.family, Host: family.host, Port: "0"}, Peer: upstream}
			c := udpClient(t, startUDP(t, l))
			payloads := [][]byte{{}, []byte("a"), bytes.Repeat([]byte{0, 1, 255, 4}, 2048), bytes.Repeat([]byte{0x91}, 60000), []byte("after large and empty packets")}
			total := 0
			for _, p := range payloads {
				udpExchange(t, c, p)
				total += len(p)
			}
			eventuallyUDP(t, func() bool { return l.Status().PeerToClientDatagrams == int64(len(payloads)) })
			st := l.Status()
			if st.CreatedSessions != 1 || st.ActiveSessions != 1 || st.ClientToPeerBytes != int64(total) || st.PeerToClientBytes != int64(total) || st.DroppedDatagrams != 0 {
				t.Fatalf("status: %+v", st)
			}
		})
	}
}

func TestUDPIPv6ToIPv4OversizedPacketPreservesSession(t *testing.T) {
	probe, err := net.ListenPacket("udp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 unavailable: %v", err)
	}
	probe.Close()
	l := &UDPListener{Iface: Endpoint{Family: FamilyIPv6, Host: "::1", Port: "0"}, Peer: udpEcho(t, "udp4", "127.0.0.1:0")}
	c := udpClient(t, startUDP(t, l))
	if _, err := c.Write(bytes.Repeat([]byte{42}, 65527)); err != nil {
		t.Fatal(err)
	}
	eventuallyUDP(t, func() bool { return l.Status().DroppedDatagrams == 1 })
	udpExchange(t, c, []byte("same session after IPv4 payload limit"))
	if l.Status().CreatedSessions != 1 {
		t.Fatal("oversized datagram destroyed the upstream mapping")
	}
}

func TestUDPClientIsolation(t *testing.T) {
	upstream := udpEcho(t, "udp4", "127.0.0.1:0")
	l := &UDPListener{Iface: Endpoint{Host: "127.0.0.1", Port: "0"}, Peer: upstream}
	addr := startUDP(t, l)
	const clients = 12
	var wg sync.WaitGroup
	for i := range clients {
		c := udpClient(t, addr)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 10 {
				udpExchange(t, c, []byte(fmt.Sprintf("client=%d packet=%d", i, j)))
			}
		}()
	}
	wg.Wait()
	eventuallyUDP(t, func() bool { return l.Status().PeerToClientDatagrams == clients*10 })
	if s := l.Status(); s.ActiveSessions != clients || s.CreatedSessions != clients {
		t.Fatalf("sessions: %+v", s)
	}
}

func TestUDPIdleReclamation(t *testing.T) {
	l := &UDPListener{Iface: Endpoint{Host: "127.0.0.1", Port: "0"}, Peer: udpEcho(t, "udp4", "127.0.0.1:0"), IdleTimeout: 120 * time.Millisecond}
	c := udpClient(t, startUDP(t, l))
	for range 12 {
		udpExchange(t, c, []byte("keep alive"))
		time.Sleep(25 * time.Millisecond)
	}
	if l.Status().CreatedSessions != 1 {
		t.Fatal("expired an active session")
	}
	eventuallyUDP(t, func() bool { return l.Status().ActiveSessions == 0 })
	udpExchange(t, c, []byte("recreated"))
	if l.Status().CreatedSessions != 2 {
		t.Fatal("did not recreate expired client")
	}
}

func TestUDPBoundedSessionsAndQueues(t *testing.T) {
	upstream := udpEcho(t, "udp4", "127.0.0.1:0")
	started := make(chan struct{})
	var calls atomic.Int32
	l := &UDPListener{Iface: Endpoint{Host: "127.0.0.1", Port: "0"}, Peer: upstream, MaxSessions: 2,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if calls.Add(1) == 1 {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	addr := startUDP(t, l)
	blocked := udpClient(t, addr)
	blocked.Write([]byte("pending dial"))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("dial did not start")
	}
	for range udpQueueSize + 4 {
		blocked.Write([]byte("queued"))
	}
	eventuallyUDP(t, func() bool { return l.Status().DroppedDatagrams > 0 })
	healthy := udpClient(t, addr)
	udpExchange(t, healthy, []byte("independent"))
	before := l.Status().DroppedDatagrams
	third := udpClient(t, addr)
	third.Write([]byte("over limit"))
	eventuallyUDP(t, func() bool { return l.Status().DroppedDatagrams > before })
	if l.Status().ActiveSessions != 2 || calls.Load() != 2 {
		t.Fatalf("unbounded admission: %+v", l.Status())
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
	if s := l.Status(); s.Listening || s.ActiveSessions != 0 || s.Address != nil {
		t.Fatalf("stopped: %+v", s)
	}
	if err := l.Start(); err == nil {
		t.Fatal("restarted stopped listener")
	}
}

func TestUDPIdleCancelsDial(t *testing.T) {
	canceled := make(chan struct{})
	l := &UDPListener{Iface: Endpoint{Host: "127.0.0.1", Port: "0"}, Peer: Endpoint{Host: "localhost", Port: "9"}, IdleTimeout: 30 * time.Millisecond,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		},
	}
	c := udpClient(t, startUDP(t, l))
	c.Write([]byte("expire"))
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("idle dial remained pending")
	}
	eventuallyUDP(t, func() bool { return l.Status().ActiveSessions == 0 })
}

func TestUDPGlobalQueueBudgetAndReclamation(t *testing.T) {
	var calls atomic.Int32
	l := &UDPListener{Iface: Endpoint{Host: "127.0.0.1", Port: "0"}, Peer: Endpoint{Host: "127.0.0.1", Port: "9"},
		MaxQueuedBytes: 8192, IdleTimeout: 100 * time.Millisecond,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			calls.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	addr := startUDP(t, l)
	first, second := udpClient(t, addr), udpClient(t, addr)
	payload := bytes.Repeat([]byte{42}, 4096)
	for _, c := range []net.Conn{first, second} {
		if _, err := c.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	eventuallyUDP(t, func() bool { return l.Status().QueuedBytes == 8192 && calls.Load() == 2 })
	if _, err := first.Write(payload); err != nil {
		t.Fatal(err)
	}
	eventuallyUDP(t, func() bool { return l.Status().DroppedDatagrams == 1 })
	if st := l.Status(); st.QueuedBytes > 8192 || st.ActiveSessions != 2 {
		t.Fatalf("queue budget: %+v", st)
	}
	eventuallyUDP(t, func() bool { st := l.Status(); return st.QueuedBytes == 0 && st.ActiveSessions == 0 })
	if _, err := first.Write(payload); err != nil {
		t.Fatal(err)
	}
	eventuallyUDP(t, func() bool { st := l.Status(); return st.QueuedBytes == len(payload) && st.CreatedSessions == 3 })
	if err := l.Stop(); err != nil {
		t.Fatal(err)
	}
	if l.Status().QueuedBytes != 0 {
		t.Fatal("Stop retained queued payloads")
	}
}

func TestUDPStopRejectsLateDial(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	started := make(chan struct{})
	l := &UDPListener{Iface: Endpoint{Host: "127.0.0.1", Port: "0"}, Peer: Endpoint{Host: "localhost", Port: "9"},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { close(started); <-ctx.Done(); return a, nil },
	}
	c := udpClient(t, startUDP(t, l))
	c.Write([]byte("late dial"))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("dial did not start")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Stop(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	b.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := b.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("late socket not closed: %v", err)
	}
}

func TestUDPFleetRollback(t *testing.T) {
	f := NewFleet(testLogger())
	l := &UDPListener{Iface: Endpoint{Host: "127.0.0.1", Port: "0"}, Peer: Endpoint{Host: "127.0.0.1", Port: "9"}}
	f.AddUDP(l)
	f.AddSocks(&SocksListener{})
	if err := f.Start(); err == nil {
		t.Fatal("expected startup failure")
	}
	if l.Status().Listening || l.Address() != nil {
		t.Fatal("failed fleet left UDP running")
	}
	if err := f.Stop(); err != nil {
		t.Fatal(err)
	}
}
