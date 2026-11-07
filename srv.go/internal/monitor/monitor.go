package monitor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hangtiancheng/wanproxy/srv.go/internal/paths"
)

type CodecStats struct {
	IncomingToCodecBytes int64 `json:"incomingToCodecBytes"`
	CodecToOutgoingBytes int64 `json:"codecToOutgoingBytes"`
	OutgoingToCodecBytes int64 `json:"outgoingToCodecBytes"`
	CodecToIncomingBytes int64 `json:"codecToIncomingBytes"`
}

type codecStatus struct {
	Mode  string      `json:"mode"`
	Stats *CodecStats `json:"stats"`
}

type ProxyStatus struct {
	Kind           string       `json:"kind"`
	Name           string       `json:"name"`
	Listening      bool         `json:"listening"`
	InterfaceCodec *codecStatus `json:"interfaceCodec"`
	PeerCodec      *codecStatus `json:"peerCodec"`
}

type FleetStatus struct {
	GeneratedAt string        `json:"generatedAt"`
	Proxies     []ProxyStatus `json:"proxies"`
}

type Scenario struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	Base        string `json:"base,omitempty"`
	Proxy       string `json:"proxy,omitempty"`
	Target      string `json:"target,omitempty"`
	StatsProxy  string `json:"statsProxy,omitempty"`
	StatsCodec  string `json:"statsCodec,omitempty"`
	Protocol    string `json:"protocol"`
}

func Scenarios() []Scenario {
	srvPort := paths.SRVPort()
	srvBase := fmt.Sprintf("http://127.0.0.1:%d", srvPort)
	srvTarget := fmt.Sprintf("127.0.0.1:%d", srvPort)
	return []Scenario{
		{
			ID:          "direct",
			Label:       "Direct HTTP",
			Description: "Browser/benchmark client hits the SRV directly, no proxy",
			Kind:        "http",
			Base:        srvBase,
			Protocol:    "TCP + HTTP/1.1",
		},
		{
			ID:          "passthrough",
			Label:       "TCP passthrough proxy",
			Description: "wanproxy TCP-to-TCP proxy with no codec (control baseline)",
			Kind:        "http",
			Base:        "http://127.0.0.1:8310",
			StatsProxy:  "proxy-passthrough",
			Protocol:    "TCP + HTTP/1.1",
		},
		{
			ID:          "zlib-chain",
			Label:       "zlib compression chain (dual proxy)",
			Description: "A-side deflate → B-side inflate, pure compression control (no dedup)",
			Kind:        "http",
			Base:        "http://127.0.0.1:8311",
			StatsProxy:  "proxy-zlib",
			StatsCodec:  "peerCodec",
			Protocol:    "TCP + HTTP/1.1 + zlib",
		},
		{
			ID:          "xcodec-chain",
			Label:       "XCodec dedup chain (dual proxy)",
			Description: "A-side XCodec encode → B-side decode, pure segment-level dedup (2048 B segments, rolling hash, no compression)",
			Kind:        "http",
			Base:        "http://127.0.0.1:8312",
			StatsProxy:  "proxy-xcodec",
			StatsCodec:  "peerCodec",
			Protocol:    "TCP + HTTP/1.1 + XCodec",
		},
		{
			ID:          "wan-chain",
			Label:       "WAN optimization chain (dual proxy)",
			Description: "Client-side proxy encodes XCodec+zlib → server-side proxy decodes; the standard WAN optimization deployment",
			Kind:        "http",
			Base:        "http://127.0.0.1:8320",
			StatsProxy:  "proxy-wan-a",
			StatsCodec:  "peerCodec",
			Protocol:    "TCP + HTTP/1.1 + XCodec + zlib",
		},
		{
			ID:          "socks",
			Label:       "SOCKS5 CONNECT proxy",
			Description: "SOCKS5 handshake then CONNECT to the SRV",
			Kind:        "socks",
			Proxy:       "127.0.0.1:8330",
			Target:      srvTarget,
			StatsProxy:  "proxy-socks",
			Protocol:    "SOCKS5 + TCP + HTTP/1.1",
		},
		{
			ID:          "ssh-chain",
			Label:       "SSH-to-SSH encrypted chain (dual proxy)",
			Description: "SSH transport (DH group-exchange + AES-128-CTR + HMAC-SHA1) carrying an XCodec+zlib WAN chain, terminating at an SSH echo server",
			Kind:        "ssh",
			Proxy:       "127.0.0.1:8340",
			StatsProxy:  "proxy-ssh-a",
			StatsCodec:  "peerCodec",
			Protocol:    "SSH-2 + XCodec + zlib",
		},
	}
}

func FindScenario(id string) (Scenario, bool) {
	for _, s := range Scenarios() {
		if s.ID == id {
			return s, true
		}
	}
	return Scenario{}, false
}

type Service struct {
	url    string
	client *http.Client
}

func NewService() *Service {
	return &Service{
		url:    fmt.Sprintf("http://127.0.0.1:%d", paths.MonitorPort()),
		client: &http.Client{Timeout: 3 * time.Second},
	}
}

func (s *Service) StatusRaw() ([]byte, error) {
	resp, err := s.client.Get(s.url + "/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("monitor status %d", resp.StatusCode)
	}
	return body, nil
}

func (s *Service) Status() (*FleetStatus, error) {
	raw, err := s.StatusRaw()
	if err != nil {
		return nil, err
	}
	var fs FleetStatus
	if err := json.Unmarshal(raw, &fs); err != nil {
		return nil, fmt.Errorf("decode monitor status: %w", err)
	}
	return &fs, nil
}

func StatsOf(fs *FleetStatus, proxyName, codecSlot string) CodecStats {
	if fs == nil {
		return CodecStats{}
	}
	for _, p := range fs.Proxies {
		if p.Name != proxyName {
			continue
		}
		var cs *codecStatus
		if codecSlot == "interfaceCodec" {
			cs = p.InterfaceCodec
		} else {
			cs = p.PeerCodec
		}
		if cs != nil && cs.Stats != nil {
			return *cs.Stats
		}
		return CodecStats{}
	}
	return CodecStats{}
}
