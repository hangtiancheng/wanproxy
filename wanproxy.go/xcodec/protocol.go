package xcodec

import (
	"encoding/binary"
	"fmt"
)

const (
	PipeOpHello byte = 0xff

	PipeOpLearn byte = 0xf1

	PipeOpAsk byte = 0xf0

	PipeOpEOS byte = 0xfc

	PipeOpEOSAck byte = 0xfb

	PipeOpFrame byte = 0x02

	PipeOpAdvance byte = 0x01
)

const (
	PipeMaxFrame = 1024 * 1024

	PipeMaxPayloadFrame = PipeMaxFrame / 2

	PipeAskMax = 512

	PipeHelloIDLength = 16
)

type PipeFrameKind int

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

type PipeFrame struct {
	Kind PipeFrameKind

	ID CacheID

	Payload []byte

	Hashes []uint64

	Segments [][]byte

	Count uint32
}

func AppendHello(output []byte, id CacheID) []byte {
	output = append(output, PipeOpHello, byte(len(id)))
	return append(output, id[:]...)
}

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

func AppendAdvance(output []byte, count uint32) []byte {
	if count == 0 {
		panic("xcodec: advance count must be positive")
	}
	output = append(output, PipeOpAdvance)
	var be [4]byte
	binary.BigEndian.PutUint32(be[:], count)
	return append(output, be[:]...)
}

func AppendEOS(output []byte) []byte { return append(output, PipeOpEOS) }

func AppendEOSAck(output []byte) []byte { return append(output, PipeOpEOSAck) }

type PipeFrameReadStatus int

const (
	PipeFrameReadOK PipeFrameReadStatus = iota
	PipeFrameReadNeedMore
	PipeFrameReadInvalid
)

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
