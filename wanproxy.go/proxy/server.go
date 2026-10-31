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

// listenerState owns the accept loop, outbound dial context and all sockets.
// Registration and shutdown share a lock so Stop cannot miss a connection.
type listenerState struct {
	mu                  sync.Mutex
	listener            net.Listener
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	active              map[net.Conn]struct{}
	acceptedConnections int64
	stopped             bool
}

func (s *listenerState) start(ep Endpoint, log *slog.Logger, name string, handle func(net.Conn)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.listener != nil {
		return fmt.Errorf("proxy %q: listener already started or stopped", name)
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
		for {
			conn, err := ln.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					log.Debug("accept stopped", "proxy", name, "error", err)
				}
				return
			}
			s.mu.Lock()
			if s.stopped {
				s.mu.Unlock()
				conn.Close()
				return
			}
			s.acceptedConnections++
			s.active[conn] = struct{}{}
			s.wg.Add(1)
			s.mu.Unlock()
			go func() {
				defer s.wg.Done()
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

// Stop cancels dialing, closes every socket and waits for handlers to exit.
// It is safe to call repeatedly or concurrently.
func (s *listenerState) Stop() error {
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

func dialPeer(ctx context.Context, dialContext func(context.Context, string, string) (net.Conn, error), dial func(string, string) (net.Conn, error), network, addr string) (net.Conn, error) {
	if dialContext != nil {
		return dialContext(ctx, network, addr)
	}
	if dial != nil {
		return dial(network, addr)
	}
	d := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return d.DialContext(ctx, network, addr)
}
