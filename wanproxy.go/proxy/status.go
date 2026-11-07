package proxy

import (
	"time"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
)

type CodecStatus struct {
	Mode            string             `json:"mode"`
	CompressorLevel int                `json:"compressorLevel,omitempty"`
	TrackStatistics bool               `json:"trackStatistics,omitempty"`
	Stats           *pipeline.Snapshot `json:"stats,omitempty"`
}

func codecStatusOf(spec *pipeline.CodecSpec) *CodecStatus {
	if spec == nil || spec.Mode == pipeline.ModeNone {
		return nil
	}
	st := &CodecStatus{
		Mode:            spec.Mode.String(),
		CompressorLevel: spec.CompressorLevel,
		TrackStatistics: spec.TrackStatistics,
	}
	if spec.TrackStatistics && spec.Stats != nil {
		snap := spec.Stats.Snapshot()
		st.Stats = &snap
	}
	return st
}

type TcpProxyStatus struct {
	Kind                string        `json:"kind"`
	Name                string        `json:"name"`
	Listen              EndpointJSON  `json:"listen"`
	Upstream            EndpointJSON  `json:"upstream"`
	InterfaceCodec      *CodecStatus  `json:"interfaceCodec,omitempty"`
	PeerCodec           *CodecStatus  `json:"peerCodec,omitempty"`
	Listening           bool          `json:"listening"`
	AcceptedConnections int64         `json:"acceptedConnections"`
	ActiveSockets       int           `json:"activeSockets"`
	Address             *BoundAddress `json:"address,omitempty"`
}

type SocksProxyStatus struct {
	Kind                string        `json:"kind"`
	Name                string        `json:"name"`
	Listen              EndpointJSON  `json:"listen"`
	Listening           bool          `json:"listening"`
	AcceptedConnections int64         `json:"acceptedConnections"`
	ActiveSockets       int           `json:"activeSockets"`
	Address             *BoundAddress `json:"address,omitempty"`
}

type UDPProxyStatus struct {
	Kind                  string        `json:"kind"`
	Name                  string        `json:"name"`
	Listen                EndpointJSON  `json:"listen"`
	Upstream              EndpointJSON  `json:"upstream"`
	Listening             bool          `json:"listening"`
	Address               *BoundAddress `json:"address,omitempty"`
	CreatedSessions       int64         `json:"createdSessions"`
	ActiveSessions        int           `json:"activeSessions"`
	ClientToPeerDatagrams int64         `json:"clientToPeerDatagrams"`
	PeerToClientDatagrams int64         `json:"peerToClientDatagrams"`
	ClientToPeerBytes     int64         `json:"clientToPeerBytes"`
	PeerToClientBytes     int64         `json:"peerToClientBytes"`
	DroppedDatagrams      int64         `json:"droppedDatagrams"`
}

type EndpointJSON struct {
	Family string `json:"family,omitempty"`
	Host   string `json:"host,omitempty"`
	Port   string `json:"port,omitempty"`
	Path   string `json:"path,omitempty"`
}

func endpointJSONOf(e Endpoint) EndpointJSON {
	if e.Family == FamilyUnix {
		return EndpointJSON{Family: string(e.Family), Path: e.Path}
	}
	return EndpointJSON{Family: string(e.Family), Host: e.Host, Port: e.Port}
}

type FleetStatus struct {
	GeneratedAt time.Time   `json:"generatedAt"`
	Proxies     interface{} `json:"proxies"`
}
