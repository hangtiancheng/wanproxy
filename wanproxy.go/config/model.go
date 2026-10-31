// Package config loads WANProxy configuration in two formats:
//
//   - the classic wanproxy.conf directive format of the C++ original
//     (create/set/activate with $object.member references), and
//   - the JSON configuration format.
//
// Both produce the same runtime model consumed by the proxy fleet.
package config

import (
	"crypto/rsa"
	"errors"
	"io"
	"log/slog"
	"sync"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/proxy"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

// SSHProxySettings holds the loaded SSH server host key for an SSH-SSH
// proxy, mirroring the C++ SSHProxyConfig.
type SSHProxySettings struct {
	HostKey     *rsa.PrivateKey
	HostKeyPath string
}

// ProxyConfig is one activated TCP-TCP proxy, mirroring the C++ "proxy"
// config class instance.
type ProxyConfig struct {
	Name           string
	Interface      proxy.Endpoint
	InterfaceCodec *pipeline.CodecSpec
	Peer           proxy.Endpoint
	PeerCodec      *pipeline.CodecSpec

	// SSH is non-nil for SSH-SSH proxies.
	SSH *SSHProxySettings
}

// SocksConfig is one activated SOCKS proxy, mirroring the C++
// "proxy-socks" config class instance.
type SocksConfig struct {
	Name      string
	Interface proxy.Endpoint
}

// MonitorConfig is one activated monitor, mirroring the C++ "monitor"
// config class instance.
type MonitorConfig struct {
	Name      string
	Interface proxy.Endpoint
}

// LogMask is one activated log-mask object, mirroring the C++
// ConfigClassLogMask instance: messages whose component matches Regex
// are logged when their level is at least as severe as Level.
type LogMask struct {
	Name  string
	Regex string
	Level slog.Level
}

// ObjectField is one field of a config object, retained for monitor
// export like the C++ ConfigObject member map.
type ObjectField struct {
	Name  string
	Type  string
	Value string
}

// Object is a created config object, retained for monitor export.
type Object struct {
	Name   string
	Class  string
	Fields []ObjectField
}

// Config is the fully loaded runtime configuration.
type Config struct {
	Proxies      []ProxyConfig
	SocksProxies []SocksConfig
	Monitor      *MonitorConfig

	// LogLevel is the most detailed level requested by log-mask
	// objects, kept as a single-level view.
	LogLevel slog.Level

	// LogMasks lists the activated log-mask objects in activation
	// order; the masks are installed on the shared logger during
	// activation, exactly like the C++ ConfigClassLogMask activation
	// calling Log::mask.
	LogMasks []LogMask

	// Objects lists every created object for monitor export.
	Objects []Object

	// Caches lists activated cache views. Close releases their owned volumes.
	Caches []xcodec.Cache

	resources []io.Closer
	closeOnce sync.Once
	closeErr  error
}

// Close releases owned disk volumes. Call it after all proxy handlers stop.
// It is safe to call repeatedly, including after partial startup failure.
func (c *Config) Close() error {
	c.closeOnce.Do(func() {
		for i := len(c.resources) - 1; i >= 0; i-- {
			c.closeErr = errors.Join(c.closeErr, c.resources[i].Close())
		}
	})
	return c.closeErr
}

// HasProxies reports whether any proxy was configured.
func (c *Config) HasProxies() bool {
	return len(c.Proxies) > 0 || len(c.SocksProxies) > 0
}
