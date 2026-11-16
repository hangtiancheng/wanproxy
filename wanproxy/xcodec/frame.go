package xcodec

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

const RefFrameSize = 2 + 8 + sha256.Size

type FrameKind int

const (
	FrameEscape FrameKind = iota
	FrameExtract
	FrameRef
	FrameBackref
)

func (k FrameKind) String() string {
	switch k {
	case FrameEscape:
		return "ESCAPE"
	case FrameExtract:
		return "EXTRACT"
	case FrameRef:
		return "REF"
	case FrameBackref:
		return "BACKREF"
	}
	return fmt.Sprintf("FRAME(%d)", int(k))
}

type Frame struct {
	Kind FrameKind

	Segment []byte

	Hash   uint64
	Digest [sha256.Size]byte

	Index int
}

var EscapeFrame = Frame{Kind: FrameEscape}

func ExtractFrame(segment []byte) Frame {
	return Frame{Kind: FrameExtract, Segment: segment}
}

func RefFrame(hash uint64, digest [sha256.Size]byte) Frame {
	return Frame{Kind: FrameRef, Hash: hash, Digest: digest}
}

func BackrefFrame(index int) Frame {
	return Frame{Kind: FrameBackref, Index: index}
}

func AppendFrame(output []byte, frame Frame) []byte {
	switch frame.Kind {
	case FrameEscape:
		return append(output, Magic, OpEscape)
	case FrameExtract:
		if len(frame.Segment) != SegmentLength {
			panic("xcodec: extract segment must be exactly 2048 bytes")
		}
		output = append(output, Magic, OpExtract)
		return append(output, frame.Segment...)
	case FrameRef:
		output = append(output, Magic, OpRef)
		var be [8]byte
		binary.BigEndian.PutUint64(be[:], frame.Hash)
		output = append(output, be[:]...)
		return append(output, frame.Digest[:]...)
	case FrameBackref:
		if frame.Index < 0 || frame.Index > WindowMax {
			panic("xcodec: backref index must be in the range 0..255")
		}
		return append(output, Magic, OpBackref, byte(frame.Index))
	}
	panic(fmt.Sprintf("xcodec: unknown frame kind %d", frame.Kind))
}

type FrameReadStatus int

const (
	FrameReadOK FrameReadStatus = iota
	FrameReadNeedMore
	FrameReadInvalid
)

func ReadFrame(buf []byte) (Frame, int, FrameReadStatus, error) {
	if len(buf) < 2 {
		return Frame{}, 0, FrameReadNeedMore, nil
	}
	if buf[0] != Magic {
		return Frame{}, 0, FrameReadInvalid, fmt.Errorf("frame does not start with XCodec magic")
	}

	switch buf[1] {
	case OpEscape:
		return EscapeFrame, 2, FrameReadOK, nil
	case OpExtract:
		if len(buf) < 2+SegmentLength {
			return Frame{}, 0, FrameReadNeedMore, nil
		}
		return ExtractFrame(buf[2 : 2+SegmentLength]), 2 + SegmentLength, FrameReadOK, nil
	case OpRef:
		if len(buf) < RefFrameSize {
			return Frame{}, 0, FrameReadNeedMore, nil
		}
		var digest [sha256.Size]byte
		copy(digest[:], buf[10:RefFrameSize])
		return RefFrame(binary.BigEndian.Uint64(buf[2:10]), digest), RefFrameSize, FrameReadOK, nil
	case OpBackref:
		if len(buf) < 3 {
			return Frame{}, 0, FrameReadNeedMore, nil
		}
		return BackrefFrame(int(buf[2])), 3, FrameReadOK, nil
	default:
		return Frame{}, 0, FrameReadInvalid, fmt.Errorf("unsupported XCodec opcode %d", buf[1])
	}
}

func ReadRawPrefix(buf []byte) int {
	for i := range buf {
		if buf[i] == Magic {
			return i
		}
	}
	return len(buf)
}
