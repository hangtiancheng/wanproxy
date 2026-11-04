// Package config loads the JSON configuration described by schema.json.
package config

import (
	"crypto/rsa"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/proxy"
)

// ProxyConfig is a validated TCP, UDP, SSH or SOCKS listener.
type ProxyConfig struct {
	Name             string
	Type             string
	Listen           proxy.Endpoint
	Upstream         proxy.Endpoint
	InterfaceCodec   *pipeline.CodecSpec
	PeerCodec        *pipeline.CodecSpec
	HostKey          *rsa.PrivateKey
	HandshakeTimeout time.Duration
	IdleTimeout      time.Duration
	MaxSessions      int
}

type LogMask struct {
	Regex string
	Level slog.Level
}

// ObjectField and Object expose named JSON sections to the HTTP monitor.
type ObjectField struct{ Name, Type, Value string }
type Object struct {
	Name   string
	Class  string
	Fields []ObjectField
}

// Config owns resources opened while loading. Stop its fleet before Close.
type Config struct {
	Proxies   []ProxyConfig
	Monitor   *proxy.Endpoint
	LogLevel  slog.Level
	LogMasks  []LogMask
	Objects   []Object
	resources []io.Closer
	closeOnce sync.Once
	closeErr  error
}

// Close releases disk volumes, including after partial startup failure.
func (c *Config) Close() error {
	c.closeOnce.Do(func() {
		for i := len(c.resources) - 1; i >= 0; i-- {
			c.closeErr = errors.Join(c.closeErr, c.resources[i].Close())
		}
	})
	return c.closeErr
}

// NewFleet constructs listeners from the validated configuration.
func (c *Config) NewFleet(log *slog.Logger) *proxy.Fleet {
	f := proxy.NewFleet(log)
	for _, p := range c.Proxies {
		proxyLog := f.Log.With("proxy", p.Name)
		switch p.Type {
		case "udp":
			f.AddUDP(&proxy.UDPListener{Name: p.Name, Iface: p.Listen, Peer: p.Upstream, IdleTimeout: p.IdleTimeout, MaxSessions: p.MaxSessions, Log: proxyLog})
		case "socks":
			f.AddSocks(&proxy.SocksListener{Name: p.Name, Iface: p.Listen, HandshakeTimeout: p.HandshakeTimeout, Log: proxyLog})
		default:
			l := &proxy.Listener{Name: p.Name, Iface: p.Listen, Peer: p.Upstream, IfaceCodec: p.InterfaceCodec, PeerCodec: p.PeerCodec, Log: proxyLog}
			if p.Type == "ssh" {
				l.SSH = &proxy.SSHSettings{HostKey: p.HostKey, HandshakeTimeout: p.HandshakeTimeout}
			}
			f.AddTCP(l)
		}
	}
	return f
}
