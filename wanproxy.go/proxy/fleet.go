package proxy

import (
	"log/slog"
	"sync"
)

type Fleet struct {
	Log *slog.Logger

	mu    sync.Mutex
	tcp   []*Listener
	udp   []*UDPListener
	socks []*SocksListener
}

func (f *Fleet) AddUDP(l *UDPListener) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.udp = append(f.udp, l)
}

func NewFleet(log *slog.Logger) *Fleet {
	if log == nil {
		log = slog.Default()
	}
	return &Fleet{Log: log}
}

func (f *Fleet) AddTCP(l *Listener) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tcp = append(f.tcp, l)
}

func (f *Fleet) AddSocks(l *SocksListener) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.socks = append(f.socks, l)
}

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
