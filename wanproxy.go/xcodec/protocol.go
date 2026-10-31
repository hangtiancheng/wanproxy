package xcodec

import (
	"encoding/binary"
	"fmt"
)

// Pipe protocol opcodes, mirroring xcodec_pipe_protocol.h.
const (
	// PipeOpHello: <OP_HELLO> length[uint8] data[length]; must appear at
	// the start of an encoded stream.
	PipeOpHello byte = 0xff

	// PipeOpLearn: <OP_LEARN> count[uint16] data[SegmentLength x count];
	// associates each segment with its hash, answering an <ASK>.
	PipeOpLearn byte = 0xf1

	// PipeOpAsk: <OP_ASK> count[uint16] hash[uint64 x count]; requests
	// the data for the given hashes, answered with <LEARN>.
	PipeOpAsk byte = 0xf0

	// PipeOpEOS: <OP_EOS>; no more data will be sent.
	PipeOpEOS byte = 0xfc

	// PipeOpEOSAck: <OP_EOS_ACK>; all sent data has been processed.
	PipeOpEOSAck byte = 0xfb

	// PipeOpFrame: <OP_FRAME> length[uint32] data[length]; frames one
	// encoded chunk.
	PipeOpFrame byte = 0x02

	// PipeOpAdvance: <OP_ADVANCE> count[uint32]; the peer processed the
	// last count frames and need not retain their references.
	PipeOpAdvance byte = 0x01
)

// Pipe protocol limits, mirroring the C++ XCODEC_PIPE_* constants.
const (
	// PipeMaxFrame is the largest encoded frame on the wire.
	PipeMaxFrame = 1024 * 1024

	// PipeMaxPayloadFrame is the largest plaintext chunk encoded into a
	// single frame; encoding at worst doubles the size of data, so each
	// frame stays self-contained within PipeMaxFrame.
	PipeMaxPayloadFrame = PipeMaxFrame / 2

	// PipeAskMax is the largest number of hashes per <ASK>/<LEARN>.
	PipeAskMax = 512

	// PipeHelloIDLength is the length of the cache identity in <HELLO>.
	PipeHelloIDLength = 16
)

// PipeFrameKind identifies a pipe protocol control frame.
type PipeFrameKind int

// Pipe protocol control frame kinds.
const (
	PipeFrameHello PipeFrameKind = iota
	PipeFrameFrame
	PipeFrameAsk
	PipeFrameLearn
	PipeFrameAdvance
	PipeFrameEOS
	PipeFrameEOSAck
)

func (k PipeFrameKind) String() string {
	switch k {
	case PipeFrameHello:
		return "HELLO"
	case PipeFrameFrame:
		return "FRAME"
	case PipeFrameAsk:
		return "ASK"
	case PipeFrameLearn:
		return "LEARN"
	case PipeFrameAdvance:
		return "ADVANCE"
	case PipeFrameEOS:
		return "EOS"
	case PipeFrameEOSAck:
		return "EOS_ACK"
	}
	return fmt.Sprintf("PIPE(%d)", int(k))
}

// PipeFrame is one parsed pipe protocol control frame.
type PipeFrame struct {
	Kind PipeFrameKind

	// ID is the peer cache identity carried by <HELLO>.
	ID CacheID

	// Payload is the encoded chunk carried by <FRAME>.
	Payload []byte

	// Hashes are the hashes requested by <ASK>.
	Hashes []uint64

	// Segments are the segments taught by <LEARN>.
	Segments [][]byte

	// Count is the frame count carried by <ADVANCE>.
	Count uint32
}

// AppendHello encodes a <HELLO> frame.
func AppendHello(output []byte, id CacheID) []byte {
	output = append(output, PipeOpHello, byte(len(id)))
	return append(output, id[:]...)
}

// AppendPipeFrame encodes a framed payload: <OP_FRAME> length data.
func AppendPipeFrame(output, payload []byte) []byte {
	if len(payload) == 0 || len(payload) > PipeMaxFrame {
		panic("xcodec: frame payload length is invalid")
	}
	output = append(output, PipeOpFrame)
	var be [4]byte
	binary.BigEndian.PutUint32(be[:], uint32(len(payload)))
	output = append(output, be[:]...)
	return append(output, payload...)
}

// AppendAsk encodes an <ASK> for the given hashes.
func AppendAsk(output []byte, hashes []uint64) []byte {
	if len(hashes) == 0 || len(hashes) > PipeAskMax {
		panic("xcodec: ask count is invalid")
	}
	output = append(output, PipeOpAsk)
	var be [2]byte
	binary.BigEndian.PutUint16(be[:], uint16(len(hashes)))
	output = append(output, be[:]...)
	var h [8]byte
	for _, hash := range hashes {
		binary.BigEndian.PutUint64(h[:], hash)
		output = append(output, h[:]...)
	}
	return output
}

// AppendLearn encodes a <LEARN> carrying whole segments.
func AppendLearn(output []byte, segments [][]byte) []byte {
	if len(segments) == 0 || len(segments) > PipeAskMax {
		panic("xcodec: learn count is invalid")
	}
	for _, segment := range segments {
		if len(segment) != SegmentLength {
			panic("xcodec: learn segment must be exactly 2048 bytes")
		}
	}
	output = append(output, PipeOpLearn)
	var be [2]byte
	binary.BigEndian.PutUint16(be[:], uint16(len(segments)))
	output = append(output, be[:]...)
	for _, segment := range segments {
		output = append(output, segment...)
	}
	return output
}

// AppendAdvance encodes an <ADVANCE> for count frames.
func AppendAdvance(output []byte, count uint32) []byte {
	if count == 0 {
		panic("xcodec: advance count must be positive")
	}
	output = append(output, PipeOpAdvance)
	var be [4]byte
	binary.BigEndian.PutUint32(be[:], count)
	return append(output, be[:]...)
}

// AppendEOS encodes an <EOS>.
func AppendEOS(output []byte) []byte { return append(output, PipeOpEOS) }

// AppendEOSAck encodes an <EOS_ACK>.
func AppendEOSAck(output []byte) []byte { return append(output, PipeOpEOSAck) }

// PipeFrameReadStatus is the outcome of reading one control frame.
type PipeFrameReadStatus int

// Control frame read outcomes.
const (
	PipeFrameReadOK PipeFrameReadStatus = iota
	PipeFrameReadNeedMore
	PipeFrameReadInvalid
)

// ReadPipeFrame parses the control frame at the front of buf, returning
// the frame, the number of bytes consumed, the read status and, when
// invalid, the reason.
func ReadPipeFrame(buf []byte) (PipeFrame, int, PipeFrameReadStatus, error) {
	if len(buf) < 1 {
		return PipeFrame{}, 0, PipeFrameReadNeedMore, nil
	}
	switch buf[0] {
	case PipeOpHello:
		if len(buf) < 2 {
			return PipeFrame{}, 0, PipeFrameReadNeedMore, nil
		}
		length := int(buf[1])
		if length != PipeHelloIDLength {
			return PipeFrame{}, 0, PipeFrameReadInvalid, fmt.Errorf("unsupported hello id length %d", length)
		}
		if len(buf) < 2+length {
			return PipeFrame{}, 0, PipeFrameReadNeedMore, nil
		}
		frame := PipeFrame{Kind: PipeFrameHello}
		copy(frame.ID[:], buf[2:2+length])
		return frame, 2 + length, PipeFrameReadOK, nil

	case PipeOpFrame:
		if len(buf) < 5 {
			return PipeFrame{}, 0, PipeFrameReadNeedMore, nil
		}
		length := binary.BigEndian.Uint32(buf[1:5])
		if length == 0 || length > PipeMaxFrame {
			return PipeFrame{}, 0, PipeFrameReadInvalid, fmt.Errorf("invalid framed data length %d", length)
		}
		if uint64(len(buf)) < 5+uint64(length) {
			return PipeFrame{}, 0, PipeFrameReadNeedMore, nil
		}
		return PipeFrame{Kind: PipeFrameFrame, Payload: buf[5 : 5+length]}, 5 + int(length), PipeFrameReadOK, nil

	case PipeOpAsk:
		count, status, err := readPipeCount(buf, "ask")
		if status != PipeFrameReadOK {
			return PipeFrame{}, 0, status, err
		}
		if len(buf) < 3+count*8 {
			return PipeFrame{}, 0, PipeFrameReadNeedMore, nil
		}
		hashes := make([]uint64, count)
		for i := 0; i < count; i++ {
			hashes[i] = binary.BigEndian.Uint64(buf[3+i*8 : 3+i*8+8])
		}
		return PipeFrame{Kind: PipeFrameAsk, Hashes: hashes}, 3 + count*8, PipeFrameReadOK, nil

	case PipeOpLearn:
		count, status, err := readPipeCount(buf, "learn")
		if status != PipeFrameReadOK {
			return PipeFrame{}, 0, status, err
		}
		if len(buf) < 3+count*SegmentLength {
			return PipeFrame{}, 0, PipeFrameReadNeedMore, nil
		}
		segments := make([][]byte, count)
		for i := 0; i < count; i++ {
			segments[i] = buf[3+i*SegmentLength : 3+(i+1)*SegmentLength]
		}
		return PipeFrame{Kind: PipeFrameLearn, Segments: segments}, 3 + count*SegmentLength, PipeFrameReadOK, nil

	case PipeOpAdvance:
		if len(buf) < 5 {
			return PipeFrame{}, 0, PipeFrameReadNeedMore, nil
		}
		count := binary.BigEndian.Uint32(buf[1:5])
		if count == 0 {
			return PipeFrame{}, 0, PipeFrameReadInvalid, fmt.Errorf("invalid advance count")
		}
		return PipeFrame{Kind: PipeFrameAdvance, Count: count}, 5, PipeFrameReadOK, nil

	case PipeOpEOS:
		return PipeFrame{Kind: PipeFrameEOS}, 1, PipeFrameReadOK, nil

	case PipeOpEOSAck:
		return PipeFrame{Kind: PipeFrameEOSAck}, 1, PipeFrameReadOK, nil

	default:
		return PipeFrame{}, 0, PipeFrameReadInvalid, fmt.Errorf("unsupported pipe protocol opcode 0x%02x", buf[0])
	}
}

func readPipeCount(buf []byte, name string) (int, PipeFrameReadStatus, error) {
	if len(buf) < 3 {
		return 0, PipeFrameReadNeedMore, nil
	}
	count := int(binary.BigEndian.Uint16(buf[1:3]))
	if count == 0 || count > PipeAskMax {
		return 0, PipeFrameReadInvalid, fmt.Errorf("invalid %s count %d", name, count)
	}
	return count, PipeFrameReadOK, nil
}
