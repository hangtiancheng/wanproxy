package proxy

import (
	"log/slog"
	"sync"
)

// Fleet runs a set of TCP, UDP and SOCKS proxies together, mirroring the set
// of ProxyListener/ProxySocksListener objects created by the C++ config
// activation.
type Fleet struct {
	Log *slog.Logger

	mu    sync.Mutex
	tcp   []*Listener
	udp   []*UDPListener
	socks []*SocksListener
}

// AddUDP registers a datagram proxy with the fleet.
func (f *Fleet) AddUDP(l *UDPListener) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.udp = append(f.udp, l)
}

// NewFleet returns an empty fleet.
func NewFleet(log *slog.Logger) *Fleet {
	if log == nil {
		log = slog.Default()
	}
	return &Fleet{Log: log}
}

// AddTCP registers a TCP proxy with the fleet.
func (f *Fleet) AddTCP(l *Listener) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tcp = append(f.tcp, l)
}

// AddSocks registers a SOCKS proxy with the fleet.
func (f *Fleet) AddSocks(l *SocksListener) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.socks = append(f.socks, l)
}

// Start starts every proxy in the fleet.
func (f *Fleet) Start() error {
	f.mu.Lock()
	tcp := append([]*Listener(nil), f.tcp...)
	udp := append([]*UDPListener(nil), f.udp...)
	socks := append([]*SocksListener(nil), f.socks...)
	f.mu.Unlock()

	var started []func() error
	rollback := func() {
		for i := len(started) - 1; i >= 0; i-- {
			_ = started[i]()
		}
	}
	for _, l := range tcp {
		if err := l.Start(); err != nil {
			rollback()
			return err
		}
		started = append(started, l.Stop)
	}
	for _, l := range udp {
		if err := l.Start(); err != nil {
			rollback()
			return err
		}
		started = append(started, l.Stop)
	}
	for _, l := range socks {
		if err := l.Start(); err != nil {
			rollback()
			return err
		}
		started = append(started, l.Stop)
	}
	return nil
}

// Stop stops every proxy in the fleet.
func (f *Fleet) Stop() error {
	f.mu.Lock()
	tcp := append([]*Listener(nil), f.tcp...)
	udp := append([]*UDPListener(nil), f.udp...)
	socks := append([]*SocksListener(nil), f.socks...)
	f.mu.Unlock()

	var first error
	for _, l := range tcp {
		if err := l.Stop(); err != nil && first == nil {
			first = err
		}
	}
	for _, l := range udp {
		if err := l.Stop(); err != nil && first == nil {
			first = err
		}
	}
	for _, l := range socks {
		if err := l.Stop(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Addresses returns the bound addresses of all listening proxies.
func (f *Fleet) Addresses() []*BoundAddress {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*BoundAddress
	for _, l := range f.udp {
		if addr := l.Address(); addr != nil {
			out = append(out, addr)
		}
	}
	for _, l := range f.tcp {
		if addr := l.Address(); addr != nil {
			out = append(out, addr)
		}
	}
	for _, l := range f.socks {
		if addr := l.Address(); addr != nil {
			out = append(out, addr)
		}
	}
	return out
}

// Status returns the status of every proxy in the fleet.
func (f *Fleet) Status() FleetStatus {
	f.mu.Lock()
	defer f.mu.Unlock()

	proxies := make([]interface{}, 0, len(f.tcp)+len(f.udp)+len(f.socks))
	for _, l := range f.udp {
		proxies = append(proxies, l.Status())
	}
	for _, l := range f.tcp {
		kind := "tcp"
		if l.SSH != nil {
			kind = "ssh"
		}
		proxies = append(proxies, TcpProxyStatus{
			Kind:                kind,
			Name:                l.Name,
			Listen:              endpointJSONOf(l.Iface),
			Upstream:            endpointJSONOf(l.Peer),
			InterfaceCodec:      codecStatusOf(l.IfaceCodec),
			PeerCodec:           codecStatusOf(l.PeerCodec),
			Listening:           l.Listening(),
			AcceptedConnections: l.AcceptedConnections(),
			ActiveSockets:       l.ActiveSockets(),
			Address:             l.Address(),
		})
	}
	for _, l := range f.socks {
		proxies = append(proxies, SocksProxyStatus{
			Kind:                "socks",
			Name:                l.Name,
			Listen:              endpointJSONOf(l.Iface),
			Listening:           l.Listening(),
			AcceptedConnections: l.AcceptedConnections(),
			ActiveSockets:       l.ActiveSockets(),
			Address:             l.Address(),
		})
	}
	return FleetStatus{
		GeneratedAt: timeNow(),
		Proxies:     proxies,
	}
}
