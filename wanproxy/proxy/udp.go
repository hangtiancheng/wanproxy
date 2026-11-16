package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"
)

const (
	DefaultUDPIdleTimeout    = 60 * time.Second
	DefaultUDPMaxSessions    = 1024
	DefaultUDPMaxQueuedBytes = 8 << 20
	udpQueueSize             = 16
	udpMaxDatagramSize       = 65535
	udpWriteTimeout          = time.Second
)

type udpPacketConn interface {
	ReadFromUDPAddrPort([]byte) (int, netip.AddrPort, error)
	WriteToUDPAddrPort([]byte, netip.AddrPort) (int, error)
	SetWriteDeadline(time.Time) error
	LocalAddr() net.Addr
	Close() error
}

type UDPListener struct {
	Name           string
	Iface          Endpoint
	Peer           Endpoint
	IdleTimeout    time.Duration
	MaxSessions    int
	MaxQueuedBytes int
	Log            *slog.Logger
	DialContext    func(context.Context, string, string) (net.Conn, error)

	mu          sync.Mutex
	conn        udpPacketConn
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	writeToken  chan struct{}
	sessions    map[netip.AddrPort]*udpSession
	stopped     bool
	stats       UDPProxyStatus
	queuedBytes int
}

type udpSession struct {
	client      netip.AddrPort
	ctx         context.Context
	cancel      context.CancelFunc
	queue       chan []byte
	conn        net.Conn
	active      time.Time
	queuedBytes int
}

func (e Endpoint) UDPNetwork() (string, error) {
	switch e.Family {
	case "", FamilyIP:
		return "udp", nil
	case FamilyIPv4:
		return "udp4", nil
	case FamilyIPv6:
		return "udp6", nil
	default:
		return "", fmt.Errorf("UDP requires an IP endpoint, got family %q", e.Family)
	}
}

func (l *UDPListener) Start() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped || l.conn != nil {
		return fmt.Errorf("UDP proxy %q: already started or stopped", l.Name)
	}
	if !l.Iface.Valid() || !l.Peer.Valid() {
		return fmt.Errorf("UDP proxy %q: incomplete endpoint", l.Name)
	}
	network, err := l.Iface.UDPNetwork()
	if err != nil {
		return err
	}
	if _, err := l.Peer.UDPNetwork(); err != nil {
		return err
	}
	if l.IdleTimeout < 0 || l.MaxSessions < 0 || l.MaxQueuedBytes < 0 {
		return fmt.Errorf("UDP proxy %q: idle timeout, session limit and queue budget must be positive", l.Name)
	}
	if l.IdleTimeout == 0 {
		l.IdleTimeout = DefaultUDPIdleTimeout
	}
	if l.MaxSessions == 0 {
		l.MaxSessions = DefaultUDPMaxSessions
	}
	if l.MaxQueuedBytes == 0 {
		l.MaxQueuedBytes = DefaultUDPMaxQueuedBytes
	}
	if l.Log == nil {
		l.Log = slog.Default()
	}
	pc, err := net.ListenPacket(network, l.Iface.Addr())
	if err != nil {
		return fmt.Errorf("UDP proxy %q: %w", l.Name, err)
	}
	conn := pc.(*net.UDPConn)
	if err := conn.SetWriteBuffer(udpMaxDatagramSize); err != nil {
		conn.Close()
		return fmt.Errorf("UDP proxy %q: send buffer: %w", l.Name, err)
	}
	l.conn = conn
	l.ctx, l.cancel = context.WithCancel(context.Background())
	l.sessions = make(map[netip.AddrPort]*udpSession)
	l.writeToken = make(chan struct{}, 1)
	l.writeToken <- struct{}{}
	l.wg.Add(2)
	go l.receive()
	go l.expire()
	return nil
}

func (l *UDPListener) receive() {
	defer l.wg.Done()
	defer l.shutdown()
	buf := make([]byte, udpMaxDatagramSize)
	var backoff time.Duration
	for {
		n, client, err := l.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			// An asynchronous ICMP error (notably on Windows) belongs to
			// a datagram, not to the shared listening socket.
			if recoverableUDPError(err) && waitNetworkRetry(l.ctx, &backoff) {
				continue
			}
			if !errors.Is(err, net.ErrClosed) {
				l.Log.Debug("UDP receive stopped", "proxy", l.Name, "error", err)
			}
			return
		}
		backoff = 0
		l.mu.Lock()
		if l.stopped {
			l.mu.Unlock()
			return
		}
		if n > l.MaxQueuedBytes-l.queuedBytes {
			l.stats.DroppedDatagrams++
			l.mu.Unlock()
			continue
		}
		s := l.sessions[client]
		if s == nil {
			if len(l.sessions) >= l.MaxSessions {
				l.stats.DroppedDatagrams++
				l.mu.Unlock()
				continue
			}
			ctx, cancel := context.WithCancel(l.ctx)
			s = &udpSession{client: client, ctx: ctx, cancel: cancel, queue: make(chan []byte, udpQueueSize)}
			l.sessions[client] = s
			l.stats.CreatedSessions++
			l.wg.Add(1)
			go l.forward(s)
		}
		s.active = time.Now()
		if len(s.queue) == cap(s.queue) {
			l.stats.DroppedDatagrams++
		} else {
			s.queue <- append([]byte{}, buf[:n]...)
			s.queuedBytes += n
			l.queuedBytes += n
		}
		l.mu.Unlock()
	}
}

func (l *UDPListener) forward(s *udpSession) {
	defer l.wg.Done()
	defer l.drop(s)
	network, _ := l.Peer.UDPNetwork()
	conn, err := dialPeer(s.ctx, l.DialContext, network, l.Peer.Addr())
	if err != nil {
		l.Log.Debug("UDP peer dial failed", "proxy", l.Name, "error", err)
		return
	}
	if udp, ok := conn.(*net.UDPConn); ok {
		if err := udp.SetWriteBuffer(udpMaxDatagramSize); err != nil {
			conn.Close()
			l.Log.Debug("UDP peer send buffer failed", "proxy", l.Name, "error", err)
			return
		}
	}
	l.mu.Lock()
	if s.ctx.Err() != nil {
		l.mu.Unlock()
		conn.Close()
		return
	}
	s.conn = conn
	l.wg.Add(1)
	l.mu.Unlock()
	go l.replies(s)
	for {
		select {
		case <-s.ctx.Done():
			return
		case data := <-s.queue:
			l.mu.Lock()
			if s.ctx.Err() != nil {
				l.mu.Unlock()
				return
			}
			s.queuedBytes -= len(data)
			l.queuedBytes -= len(data)
			l.mu.Unlock()
			if err := conn.SetWriteDeadline(time.Now().Add(min(l.IdleTimeout, udpWriteTimeout))); err != nil {
				return
			}
			n, err := conn.Write(data)
			if err != nil || n != len(data) {
				l.countUDPDrop()
				if recoverableUDPError(err) {
					continue
				}
				return
			}
			l.mu.Lock()
			s.active = time.Now()
			l.stats.ClientToPeerDatagrams++
			l.stats.ClientToPeerBytes += int64(n)
			l.mu.Unlock()
		}
	}
}

func (l *UDPListener) replies(s *udpSession) {
	defer l.wg.Done()
	defer l.drop(s)
	buf := make([]byte, udpMaxDatagramSize)
	var backoff time.Duration
	for {
		n, err := s.conn.Read(buf)
		if err != nil {
			if recoverableUDPError(err) && waitNetworkRetry(s.ctx, &backoff) {
				continue
			}
			return
		}
		backoff = 0
		l.mu.Lock()
		if s.ctx.Err() != nil {
			l.mu.Unlock()
			return
		}
		s.active = time.Now()
		l.mu.Unlock()
		written, err := l.writeReply(s, buf[:n])
		if err != nil || written != n {
			l.countUDPDrop()
			if recoverableUDPError(err) {
				continue
			}
			return
		}
		l.mu.Lock()
		l.stats.PeerToClientDatagrams++
		l.stats.PeerToClientBytes += int64(n)
		l.mu.Unlock()
	}
}

func (l *UDPListener) writeReply(s *udpSession, data []byte) (int, error) {
	select {
	case <-s.ctx.Done():
		return 0, s.ctx.Err()
	case <-l.writeToken:
	}
	defer func() { l.writeToken <- struct{}{} }()
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if err := l.conn.SetWriteDeadline(time.Now().Add(min(l.IdleTimeout, udpWriteTimeout))); err != nil {
		return 0, err
	}
	// Expiry must interrupt an in-progress reply too. Join the callback
	// before releasing writeToken so it cannot expire the next writer.
	canceled := make(chan struct{})
	stop := context.AfterFunc(s.ctx, func() {
		_ = l.conn.SetWriteDeadline(time.Now())
		close(canceled)
	})
	defer func() {
		if !stop() {
			<-canceled
		}
	}()
	return l.conn.WriteToUDPAddrPort(data, s.client)
}

func (l *UDPListener) countUDPDrop() {
	l.mu.Lock()
	l.stats.DroppedDatagrams++
	l.mu.Unlock()
}

func recoverableUDPError(err error) bool {
	if err == nil || errors.Is(err, net.ErrClosed) {
		return false
	}
	for _, errno := range []error{syscall.EMSGSIZE, syscall.ENOBUFS, syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ENETUNREACH, syscall.EHOSTUNREACH} {
		if errors.Is(err, errno) {
			return true
		}
	}
	if platformUDPError(err) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && (ne.Timeout() || ne.Temporary())
}

func (l *UDPListener) expire() {
	defer l.wg.Done()
	ticker := time.NewTicker(min(l.IdleTimeout, time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			l.mu.Lock()
			for _, s := range l.sessions {
				if time.Since(s.active) >= l.IdleTimeout {
					l.dropLocked(s)
				}
			}
			l.mu.Unlock()
		}
	}
}

func (l *UDPListener) drop(s *udpSession) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dropLocked(s)
}

func (l *UDPListener) dropLocked(s *udpSession) {
	if l.sessions[s.client] != s {
		return
	}
	delete(l.sessions, s.client)
	s.cancel()
	l.queuedBytes -= s.queuedBytes
	s.queuedBytes = 0
	// Pending dials may take time to return; do not retain their payloads.
drain:
	for {
		select {
		case <-s.queue:
		default:
			break drain
		}
	}
	if s.conn != nil {
		s.conn.Close()
	}
}

func (l *UDPListener) shutdown() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopped = true
	if l.cancel != nil {
		l.cancel()
	}
	for _, s := range l.sessions {
		l.dropLocked(s)
	}
	if l.conn != nil {
		if err := l.conn.Close(); !errors.Is(err, net.ErrClosed) {
			return err
		}
	}
	return nil
}

func (l *UDPListener) Stop() error {
	err := l.shutdown()
	l.wg.Wait()
	return err
}

func (l *UDPListener) addressLocked() *BoundAddress {
	if l.conn == nil || l.stopped {
		return nil
	}
	addr := l.conn.LocalAddr().(*net.UDPAddr)
	return &BoundAddress{Address: addr.IP.String(), Port: addr.Port}
}

func (l *UDPListener) Address() *BoundAddress {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.addressLocked()
}

func (l *UDPListener) Status() UDPProxyStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.stats
	s.Kind, s.Name = "udp", l.Name
	s.Listen, s.Upstream = endpointJSONOf(l.Iface), endpointJSONOf(l.Peer)
	s.Listening = l.conn != nil && !l.stopped
	s.Address = l.addressLocked()
	s.ActiveSessions = len(l.sessions)
	s.QueuedBytes = l.queuedBytes
	return s
}
