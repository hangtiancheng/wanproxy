package xcodec

import (
	"encoding/binary"
	"fmt"
)

// FrameKind identifies the type of an XCodec stream frame.
type FrameKind int

// XCodec stream frame kinds.
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

// Frame is one parsed XCodec stream frame.
type Frame struct {
	Kind FrameKind

	// Segment holds the declared data for FrameExtract.
	Segment []byte

	// Hash holds the referenced hash for FrameRef.
	Hash uint64

	// Index holds the window index for FrameBackref.
	Index int
}

// EscapeFrame is the shared <ESCAPE> frame value.
var EscapeFrame = Frame{Kind: FrameEscape}

// ExtractFrame builds an <EXTRACT> frame for a segment.
func ExtractFrame(segment []byte) Frame {
	return Frame{Kind: FrameExtract, Segment: segment}
}

// RefFrame builds a <REF> frame for a hash.
func RefFrame(hash uint64) Frame {
	return Frame{Kind: FrameRef, Hash: hash}
}

// BackrefFrame builds a <BACKREF> frame for a window index.
func BackrefFrame(index int) Frame {
	return Frame{Kind: FrameBackref, Index: index}
}

// AppendFrame encodes a frame onto output, mirroring the C++ wire
// format.  It panics on invalid frame contents.
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
		return append(output, be[:]...)
	case FrameBackref:
		if frame.Index < 0 || frame.Index > WindowMax {
			panic("xcodec: backref index must be in the range 0..255")
		}
		return append(output, Magic, OpBackref, byte(frame.Index))
	}
	panic(fmt.Sprintf("xcodec: unknown frame kind %d", frame.Kind))
}

// FrameReadStatus is the outcome of reading one frame from a buffer.
type FrameReadStatus int

// Frame read outcomes.
const (
	// FrameReadOK means a complete frame was parsed.
	FrameReadOK FrameReadStatus = iota
	// FrameReadNeedMore means the buffer holds a partial frame.
	FrameReadNeedMore
	// FrameReadInvalid means the stream is malformed.
	FrameReadInvalid
)

// ReadFrame parses the frame at the front of buf.  On FrameReadOK the
// frame and the number of bytes consumed are returned; on
// FrameReadInvalid an error reason is returned.  Raw (non-magic) prefix
// bytes are not handled here; see ReadRawPrefix.
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
		if len(buf) < 10 {
			return Frame{}, 0, FrameReadNeedMore, nil
		}
		return RefFrame(binary.BigEndian.Uint64(buf[2:10])), 10, FrameReadOK, nil
	case OpBackref:
		if len(buf) < 3 {
			return Frame{}, 0, FrameReadNeedMore, nil
		}
		return BackrefFrame(int(buf[2])), 3, FrameReadOK, nil
	default:
		return Frame{}, 0, FrameReadInvalid, fmt.Errorf("unsupported XCodec opcode %d", buf[1])
	}
}

// ReadRawPrefix returns the length of the literal (non-frame) prefix of
// buf: everything before the next magic byte, or the whole buffer when
// no magic byte is present.  It returns 0 when buf starts with the magic
// byte or is empty.
func ReadRawPrefix(buf []byte) int {
	for i := range buf {
		if buf[i] == Magic {
			return i
		}
	}
	return len(buf)
}
