package proxy

import (
	"fmt"
	"net"
	"strconv"
)

type Family string

const (
	FamilyIP   Family = "IP"
	FamilyIPv4 Family = "IPv4"
	FamilyIPv6 Family = "IPv6"
	FamilyUnix Family = "Unix"
)

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

type Endpoint struct {
	Family Family
	Host   string
	Port   string
	Path   string
}

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

func (e Endpoint) Addr() string {
	if e.Family == FamilyUnix {
		return e.Path
	}
	return net.JoinHostPort(e.Host, e.Port)
}

func (e Endpoint) Valid() bool {
	if e.Family == FamilyUnix {
		return e.Path != ""
	}
	return e.Host != "" && e.Port != ""
}

func FormatEndpoint(e Endpoint) string {
	if e.Family == FamilyUnix {
		return e.Path
	}
	return net.JoinHostPort(e.Host, e.Port)
}

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
