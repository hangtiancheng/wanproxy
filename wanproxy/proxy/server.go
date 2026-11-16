package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

type listenerState struct {
	mu                  sync.Mutex
	listener            net.Listener
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	active              map[net.Conn]struct{}
	acceptedConnections int64
	stopped             bool
	activeClients       int
}

const DefaultMaxConnections = 1024

func proxyLogger(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.Default()
	}
	return log
}

func (s *listenerState) start(ep Endpoint, log *slog.Logger, name string, maxConnections int, handle func(net.Conn)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.listener != nil {
		return fmt.Errorf("proxy %q: listener already started or stopped", name)
	}
	if maxConnections < 0 {
		return fmt.Errorf("proxy %q: negative connection limit", name)
	}
	if maxConnections == 0 {
		maxConnections = DefaultMaxConnections
	}
	ln, err := net.Listen(ep.Network(), ep.Addr())
	if err != nil {
		return fmt.Errorf("proxy %q: %w", name, err)
	}
	s.listener = ln
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.active = make(map[net.Conn]struct{})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.shutdown()
		var backoff time.Duration
		for {
			conn, err := ln.Accept()
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Temporary() && waitNetworkRetry(s.ctx, &backoff) {
					continue
				}
				if !errors.Is(err, net.ErrClosed) {
					log.Debug("accept stopped", "proxy", name, "error", err)
				}
				return
			}
			backoff = 0
			s.mu.Lock()
			if s.stopped {
				s.mu.Unlock()
				conn.Close()
				return
			}
			if s.activeClients >= maxConnections {
				s.mu.Unlock()
				conn.Close()
				continue
			}
			s.activeClients++
			s.acceptedConnections++
			s.active[conn] = struct{}{}
			s.wg.Add(1)
			s.mu.Unlock()
			go func() {
				defer s.wg.Done()
				defer func() { s.mu.Lock(); s.activeClients--; s.mu.Unlock() }()
				handle(conn)
			}()
		}
	}()
	return nil
}

func (s *listenerState) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		conn.Close()
		return false
	}
	s.active[conn] = struct{}{}
	return true
}

func (s *listenerState) drop(conn net.Conn) {
	conn.Close()
	s.mu.Lock()
	delete(s.active, conn)
	s.mu.Unlock()
}

func (s *listenerState) shutdown() error {
	s.mu.Lock()
	s.stopped = true
	if s.cancel != nil {
		s.cancel()
	}
	ln := s.listener
	conns := make([]net.Conn, 0, len(s.active))
	for conn := range s.active {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	var err error
	if ln != nil {
		err = ln.Close()
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
	}
	for _, conn := range conns {
		conn.Close()
	}
	return err
}

func (s *listenerState) Stop() error {
	err := s.shutdown()
	s.wg.Wait()
	return err
}

func (s *listenerState) Listening() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listener != nil && !s.stopped
}

func (s *listenerState) Address() *BoundAddress {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil
	}
	return boundAddressOf(s.listener)
}

func (s *listenerState) AcceptedConnections() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acceptedConnections
}

func (s *listenerState) ActiveSockets() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

func dialPeer(ctx context.Context, dialContext func(context.Context, string, string) (net.Conn, error), network, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if dialContext == nil {
		d := &net.Dialer{KeepAlive: 30 * time.Second}
		dialContext = d.DialContext
	}
	conn, err := dialContext(ctx, network, addr)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && conn == nil {
		err = errors.New("proxy: dial returned no connection")
	}
	if err != nil && conn != nil {
		conn.Close()
		conn = nil
	}
	return conn, err
}
