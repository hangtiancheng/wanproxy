package pipeline

import (
	"compress/zlib"
	"fmt"
	"sync/atomic"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

type Mode int

const (
	ModeNone Mode = iota
	ModeZlib
	ModeXCodec
	ModeZlibXCodec
)

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

type Role int

const (
	RoleIncoming Role = iota
	RoleOutgoing
)

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

type Direction int

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

type Step string

const (
	StepPassThrough  Step = "pass-through"
	StepInflate      Step = "inflate"
	StepDeflate      Step = "deflate"
	StepXCodecEncode Step = "xcodec-encode"
	StepXCodecDecode Step = "xcodec-decode"
)

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

func towardWAN(role Role, dir Direction) bool {
	if role == RoleIncoming {
		return dir == DirectionSend
	}
	return dir == DirectionReceive
}

type CodecSpec struct {
	Mode Mode

	CompressorLevel int

	Cache xcodec.Cache

	TrackStatistics bool

	Stats *CodecStats
}

func (c *CodecSpec) Validate() error {
	if c == nil {
		return nil
	}
	if c.Mode < ModeNone || c.Mode > ModeZlibXCodec {
		return fmt.Errorf("pipeline: invalid codec mode %d", c.Mode)
	}
	if c.NeedsSession() && c.Cache == nil {
		return fmt.Errorf("pipeline: XCodec requires a cache")
	}
	if c.NeedsCompressor() && (c.CompressorLevel < zlib.HuffmanOnly || c.CompressorLevel > zlib.BestCompression) {
		return fmt.Errorf("pipeline: invalid compressor level %d", c.CompressorLevel)
	}
	return nil
}

func (c *CodecSpec) NeedsSession() bool {
	return c != nil && (c.Mode == ModeXCodec || c.Mode == ModeZlibXCodec)
}

func (c *CodecSpec) NeedsCompressor() bool {
	return c != nil && (c.Mode == ModeZlib || c.Mode == ModeZlibXCodec)
}

type CodecStats struct {
	IncomingToCodec atomic.Int64
	CodecToOutgoing atomic.Int64
	OutgoingToCodec atomic.Int64
	CodecToIncoming atomic.Int64
}

type Snapshot struct {
	IncomingToCodec int64 `json:"incomingToCodecBytes"`
	CodecToOutgoing int64 `json:"codecToOutgoingBytes"`
	OutgoingToCodec int64 `json:"outgoingToCodecBytes"`
	CodecToIncoming int64 `json:"codecToIncomingBytes"`
}

func (s *CodecStats) Snapshot() Snapshot {
	return Snapshot{
		IncomingToCodec: s.IncomingToCodec.Load(),
		CodecToOutgoing: s.CodecToOutgoing.Load(),
		OutgoingToCodec: s.OutgoingToCodec.Load(),
		CodecToIncoming: s.CodecToIncoming.Load(),
	}
}
