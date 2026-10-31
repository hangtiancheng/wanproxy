package proxy

import (
	"fmt"
	"net"
	"strconv"
)

// Family mirrors the C++ SocketAddressFamily values used by the config
// system.
type Family string

// Address families.
const (
	FamilyIP   Family = "IP"
	FamilyIPv4 Family = "IPv4"
	FamilyIPv6 Family = "IPv6"
	FamilyUnix Family = "Unix"
)

// ParseFamily maps a C++ address family name.
func ParseFamily(name string) (Family, error) {
	switch name {
	case "IP", "":
		return FamilyIP, nil
	case "IPv4":
		return FamilyIPv4, nil
	case "IPv6":
		return FamilyIPv6, nil
	case "Unix":
		return FamilyUnix, nil
	}
	return "", fmt.Errorf("proxy: unknown address family %q", name)
}

// Endpoint is one listen or peer address, mirroring the C++
// ConfigClassAddress instance (family, host, port, path).
type Endpoint struct {
	Family Family
	Host   string
	Port   string
	Path   string
}

// Network returns the Go network type for the endpoint family.
func (e Endpoint) Network() string {
	switch e.Family {
	case FamilyIPv4:
		return "tcp4"
	case FamilyIPv6:
		return "tcp6"
	case FamilyUnix:
		return "unix"
	default:
		return "tcp"
	}
}

// Addr returns the dial/listen address, mirroring the C++
// "[host]:port" socket address construction.
func (e Endpoint) Addr() string {
	if e.Family == FamilyUnix {
		return e.Path
	}
	return net.JoinHostPort(e.Host, e.Port)
}

// Valid reports whether the endpoint can be used.
func (e Endpoint) Valid() bool {
	if e.Family == FamilyUnix {
		return e.Path != ""
	}
	return e.Host != "" && e.Port != ""
}

// FormatEndpoint renders host:port for status output.
func FormatEndpoint(e Endpoint) string {
	if e.Family == FamilyUnix {
		return e.Path
	}
	return e.Host + ":" + e.Port
}

// BoundAddress is a resolved listening address.
type BoundAddress struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

func boundAddressOf(ln net.Listener) *BoundAddress {
	if ln == nil {
		return nil
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return nil
	}
	return &BoundAddress{Address: addr.IP.String(), Port: addr.Port}
}

func parsePort(s string) (int, error) {
	return strconv.Atoi(s)
}
