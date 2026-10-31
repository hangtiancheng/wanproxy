// Package pipeline composes codec transforms for proxied connections,
// mirroring the C++ WANProxyCodecPipePair wiring: optional zlib
// compression around an XCodec pipe protocol session, applied in the
// correct order per direction and per side of the proxy.
package pipeline

import (
	"fmt"
	"sync/atomic"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

// Mode selects the codec pipeline mode, mirroring the C++ codec and
// compressor settings combined.
type Mode int

// Pipeline modes.
const (
	ModeNone Mode = iota
	ModeZlib
	ModeXCodec
	ModeZlibXCodec
)

// ParseMode maps a mode name to a Mode, accepting both the C++-style
// combinations and the JSON config names used by the TypeScript port.
func ParseMode(name string) (Mode, error) {
	switch name {
	case "none", "None", "":
		return ModeNone, nil
	case "zlib":
		return ModeZlib, nil
	case "xcodec", "XCodec":
		return ModeXCodec, nil
	case "zlib+xcodec", "zlib-xcodec", "zlibxcodec":
		return ModeZlibXCodec, nil
	}
	return ModeNone, fmt.Errorf("pipeline: unknown codec mode %q", name)
}

func (m Mode) String() string {
	switch m {
	case ModeNone:
		return "none"
	case ModeZlib:
		return "zlib"
	case ModeXCodec:
		return "xcodec"
	case ModeZlibXCodec:
		return "zlib+xcodec"
	}
	return fmt.Sprintf("mode(%d)", int(m))
}

// Role selects which side of the proxy the codec sits on, mirroring the
// C++ interface_codec / peer_codec distinction:
//
//   - RoleIncoming: the codec is the peer codec (C++ peer_codec,
//     XCodecPipePairTypeClient); the WAN side is the upstream side.
//   - RoleOutgoing: the codec is the interface codec (C++
//     interface_codec, XCodecPipePairTypeServer); the WAN side is the
//     listen/client side.
type Role int

// Pipeline roles.
const (
	RoleIncoming Role = iota
	RoleOutgoing
)

// ParseRole maps a role name to a Role.
func ParseRole(name string) (Role, error) {
	switch name {
	case "incoming", "":
		return RoleIncoming, nil
	case "outgoing":
		return RoleOutgoing, nil
	}
	return RoleIncoming, fmt.Errorf("pipeline: unknown codec role %q", name)
}

func (r Role) String() string {
	if r == RoleOutgoing {
		return "outgoing"
	}
	return "incoming"
}

// Direction selects the data flow through the proxy.
type Direction int

// Pipeline directions, relative to the proxy: Send is listen→upstream,
// Receive is upstream→listen.
const (
	DirectionSend Direction = iota
	DirectionReceive
)

func (d Direction) String() string {
	if d == DirectionReceive {
		return "receive"
	}
	return "send"
}

// Step is one transform in a resolved pipeline.
type Step string

// Pipeline steps.
const (
	StepPassThrough  Step = "pass-through"
	StepInflate      Step = "inflate"
	StepDeflate      Step = "deflate"
	StepXCodecEncode Step = "xcodec-encode"
	StepXCodecDecode Step = "xcodec-decode"
)

// ResolveSteps returns the ordered transform steps for one direction of
// a single codec, following the C++ WANProxyCodecPipePair ordering: data
// flowing toward the WAN is encoded then deflated, data flowing in from
// the WAN is inflated then decoded.
func ResolveSteps(mode Mode, role Role, dir Direction) []Step {
	switch mode {
	case ModeNone:
		return []Step{StepPassThrough}
	case ModeZlib:
		if towardWAN(role, dir) {
			return []Step{StepDeflate}
		}
		return []Step{StepInflate}
	case ModeXCodec:
		if towardWAN(role, dir) {
			return []Step{StepXCodecEncode}
		}
		return []Step{StepXCodecDecode}
	case ModeZlibXCodec:
		if towardWAN(role, dir) {
			return []Step{StepXCodecEncode, StepDeflate}
		}
		return []Step{StepInflate, StepXCodecDecode}
	}
	return []Step{StepPassThrough}
}

// towardWAN reports whether data in the given direction flows toward the
// WAN side for a codec with the given role.
func towardWAN(role Role, dir Direction) bool {
	if role == RoleIncoming {
		// WAN side is upstream: Send flows toward the WAN.
		return dir == DirectionSend
	}
	// WAN side is the listen side: Receive flows toward the WAN.
	return dir == DirectionReceive
}

// CodecSpec is one activated codec instance: the C++ "codec" config
// object (XCodec on/off, zlib compressor on/off, level, cache).
type CodecSpec struct {
	// Mode is the pipeline mode.
	Mode Mode

	// CompressorLevel is the zlib level, -1 for the default, used when
	// the mode includes compression.
	CompressorLevel int

	// Cache backs the XCodec session; required when Mode includes
	// XCodec.
	Cache xcodec.Cache

	// TrackStatistics enables the byte counters, mirroring the C++
	// track_statistics setting.
	TrackStatistics bool

	// Stats holds the byte counters when TrackStatistics is set.
	Stats *CodecStats
}

// NeedsSession reports whether the codec runs an XCodec pipe session.
func (c *CodecSpec) NeedsSession() bool {
	return c != nil && (c.Mode == ModeXCodec || c.Mode == ModeZlibXCodec)
}

// NeedsCompressor reports whether the codec compresses the wire stream.
func (c *CodecSpec) NeedsCompressor() bool {
	return c != nil && (c.Mode == ModeZlib || c.Mode == ModeZlibXCodec)
}

// CodecStats mirrors the C++ PipeByteCount counters around a codec:
// bytes crossing each of the codec's four boundaries.
type CodecStats struct {
	// IncomingToCodec counts bytes entering from the interface side.
	IncomingToCodec atomic.Int64
	// CodecToOutgoing counts bytes leaving toward the peer side.
	CodecToOutgoing atomic.Int64
	// OutgoingToCodec counts bytes entering from the peer side.
	OutgoingToCodec atomic.Int64
	// CodecToIncoming counts bytes leaving toward the interface side.
	CodecToIncoming atomic.Int64
}

// Snapshot is a point-in-time copy of the counters.
type Snapshot struct {
	IncomingToCodec int64 `json:"incomingToCodecBytes"`
	CodecToOutgoing int64 `json:"codecToOutgoingBytes"`
	OutgoingToCodec int64 `json:"outgoingToCodecBytes"`
	CodecToIncoming int64 `json:"codecToIncomingBytes"`
}

// Snapshot reads the counters.
func (s *CodecStats) Snapshot() Snapshot {
	return Snapshot{
		IncomingToCodec: s.IncomingToCodec.Load(),
		CodecToOutgoing: s.CodecToOutgoing.Load(),
		OutgoingToCodec: s.OutgoingToCodec.Load(),
		CodecToIncoming: s.CodecToIncoming.Load(),
	}
}
