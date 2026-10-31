package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/proxy"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

// JSONNetworkEndpoint is the {"host": ..., "port": ...} form used by the
// JSON configuration.
type JSONNetworkEndpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// JSONCodecConfig is the per-proxy codec block of the JSON format.
type JSONCodecConfig struct {
	Mode            string `json:"mode"`
	Role            string `json:"role"`
	CompressorLevel *int   `json:"compressorLevel,omitempty"`
}

// JSONTcpProxyConfig is one entry of the "proxies" array.
type JSONTcpProxyConfig struct {
	Name     string              `json:"name"`
	Listen   JSONNetworkEndpoint `json:"listen"`
	Upstream JSONNetworkEndpoint `json:"upstream"`
	Codec    *JSONCodecConfig    `json:"codec,omitempty"`
}

// JSONSocksProxyConfig is one entry of the "socksProxies" array.
type JSONSocksProxyConfig struct {
	Name   string              `json:"name"`
	Listen JSONNetworkEndpoint `json:"listen"`
}

// JSONMonitorConfig is the "monitor" block.
type JSONMonitorConfig struct {
	Listen JSONNetworkEndpoint `json:"listen"`
}

// JSONFileConfig is the whole JSON configuration file.
type JSONFileConfig struct {
	Proxies      []JSONTcpProxyConfig   `json:"proxies,omitempty"`
	SocksProxies []JSONSocksProxyConfig `json:"socksProxies,omitempty"`
	Monitor      *JSONMonitorConfig     `json:"monitor,omitempty"`
}

// ParseJSON parses the JSON configuration format.  The codec role maps
// onto the C++ model: "incoming"
// attaches the codec to the peer (upstream) side, "outgoing" attaches it
// to the interface (listen) side.
func ParseJSON(data []byte) (*Config, error) {
	var file JSONFileConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("invalid JSON configuration: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid JSON configuration: trailing content")
	}

	cfg := &Config{LogLevel: slog.LevelInfo}

	for i, p := range file.Proxies {
		if p.Name == "" {
			return nil, fmt.Errorf("proxies[%d]: name is required", i)
		}
		if err := validateEndpoint(p.Listen, fmt.Sprintf("proxies[%d].listen", i)); err != nil {
			return nil, err
		}
		if err := validateEndpoint(p.Upstream, fmt.Sprintf("proxies[%d].upstream", i)); err != nil {
			return nil, err
		}

		pc := ProxyConfig{
			Name:      p.Name,
			Interface: endpointOf(p.Listen),
			Peer:      endpointOf(p.Upstream),
		}

		if p.Codec != nil {
			mode, err := pipeline.ParseMode(p.Codec.Mode)
			if err != nil {
				return nil, fmt.Errorf("proxies[%d].codec: %w", i, err)
			}
			role, err := pipeline.ParseRole(p.Codec.Role)
			if err != nil {
				return nil, fmt.Errorf("proxies[%d].codec: %w", i, err)
			}
			spec := &pipeline.CodecSpec{Mode: mode, CompressorLevel: -1}
			if p.Codec.CompressorLevel != nil {
				level := *p.Codec.CompressorLevel
				if level < -2 || level > 9 {
					return nil, fmt.Errorf("proxies[%d].codec: compressorLevel %d out of range -2..9", i, level)
				}
				spec.CompressorLevel = level
			}
			if mode == pipeline.ModeXCodec || mode == pipeline.ModeZlibXCodec {
				spec.Cache = xcodec.NewMemoryCache()
				cfg.Caches = append(cfg.Caches, spec.Cache)
			}
			if role == pipeline.RoleIncoming {
				pc.PeerCodec = spec
			} else {
				pc.InterfaceCodec = spec
			}
		}

		cfg.Proxies = append(cfg.Proxies, pc)
	}

	for i, s := range file.SocksProxies {
		if s.Name == "" {
			return nil, fmt.Errorf("socksProxies[%d]: name is required", i)
		}
		if err := validateEndpoint(s.Listen, fmt.Sprintf("socksProxies[%d].listen", i)); err != nil {
			return nil, err
		}
		cfg.SocksProxies = append(cfg.SocksProxies, SocksConfig{
			Name:      s.Name,
			Interface: endpointOf(s.Listen),
		})
	}

	if file.Monitor != nil {
		if err := validateEndpoint(file.Monitor.Listen, "monitor.listen"); err != nil {
			return nil, err
		}
		cfg.Monitor = &MonitorConfig{
			Name:      "monitor",
			Interface: endpointOf(file.Monitor.Listen),
		}
	}

	if !cfg.HasProxies() {
		return nil, fmt.Errorf("at least one proxy or SOCKS proxy is required")
	}
	return cfg, nil
}

func validateEndpoint(e JSONNetworkEndpoint, what string) error {
	if e.Host == "" {
		return fmt.Errorf("%s: host is required", what)
	}
	if e.Port < 0 || e.Port > 65535 {
		return fmt.Errorf("%s: port %d out of range 0..65535", what, e.Port)
	}
	return nil
}

func endpointOf(e JSONNetworkEndpoint) proxy.Endpoint {
	return proxy.Endpoint{
		Family: proxy.FamilyIP,
		Host:   e.Host,
		Port:   strconv.Itoa(e.Port),
	}
}
