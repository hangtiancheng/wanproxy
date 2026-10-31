// Package ssh is a Go port of the WANProxy SSH-2 transport layer
// (ssh/ and programs/wanproxy/ssh_* in the C++ original).
//
// The implementation mirrors the C++ behaviour: version exchange,
// KEXINIT negotiation, diffie-hellman-group-exchange key exchange with
// the C++ test group, RFC 4253 key derivation, binary packets with
// encrypt-then-MAC ordering, and application data framing (0xff-tagged
// packets when a codec is active, length-prefixed framing otherwise).
//
// One deliberate fix over the C++ original: the C++ code never sends
// the first NEWKEYS message (acknowledged by XXX comments), leaving the
// data phase unencrypted.  This port sends NEWKEYS after key exchange,
// as the RFC requires; the C++ NEWKEYS handler replies and activates
// keys, so the two implementations interoperate in both directions.
package ssh

import (
	"encoding/binary"
	"errors"
	"math/big"
)

// SSH message numbers, mirroring ssh_protocol.h.
const (
	MsgDisconnect     = 1
	MsgIgnore         = 2
	MsgUnimplemented  = 3
	MsgDebug          = 4
	MsgServiceRequest = 5
	MsgServiceAccept  = 6
	MsgKexInit        = 20
	MsgNewKeys        = 21
	MsgKexFirstMethod = 30
	MsgKexLastMethod  = 49
	MsgLocalFirst     = 192
	appPacketTag      = 0xff // SSHStreamPacket in the C++ code
)

// Diffie-Hellman group exchange message numbers.
const (
	kexDHGroupExchangeRequest    = 34
	kexDHGroupExchangeGroup      = 31
	kexDHGroupExchangeInitialize = 32
	kexDHGroupExchangeReply      = 33
)

// DH group size bounds, mirroring DH_GROUP_MIN/DH_GROUP_MAX.
const (
	dhGroupMin = 1024
	dhGroupMax = 8192
)

var errMalformed = errors.New("ssh: malformed message")

// appendString encodes an SSH string (uint32 length + bytes).
func appendString(out []byte, s []byte) []byte {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(s)))
	out = append(out, lenBuf[:]...)
	return append(out, s...)
}

// readString decodes an SSH string, returning the payload and the
// remaining input.
func readString(in []byte) ([]byte, []byte, error) {
	if len(in) < 4 {
		return nil, nil, errMalformed
	}
	n := binary.BigEndian.Uint32(in[:4])
	if uint64(n) > uint64(len(in)-4) {
		return nil, nil, errMalformed
	}
	return in[4 : 4+n], in[4+n:], nil
}

func appendUint32(out []byte, v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return append(out, b[:]...)
}

func readUint32(in []byte) (uint32, []byte, error) {
	if len(in) < 4 {
		return 0, nil, errMalformed
	}
	return binary.BigEndian.Uint32(in[:4]), in[4:], nil
}

// appendMPInt encodes a non-negative big-endian two's-complement mpint.
func appendMPInt(out []byte, v *big.Int) []byte {
	if v.Sign() < 0 {
		panic("ssh: negative mpint not supported")
	}
	b := v.Bytes()
	if len(b) > 0 && b[0]&0x80 != 0 {
		b = append([]byte{0x00}, b...)
	}
	return appendString(out, b)
}

// readMPInt decodes an mpint as a non-negative magnitude.
func readMPInt(in []byte) (*big.Int, []byte, error) {
	b, rest, err := readString(in)
	if err != nil {
		return nil, nil, err
	}
	return new(big.Int).SetBytes(b), rest, nil
}

// appendNameList encodes a comma-separated name-list.
func appendNameList(out []byte, names []string) []byte {
	var merged []byte
	for i, name := range names {
		if i > 0 {
			merged = append(merged, ',')
		}
		merged = append(merged, name...)
	}
	return appendString(out, merged)
}

// readNameList decodes a name-list.
func readNameList(in []byte) ([]string, []byte, error) {
	merged, rest, err := readString(in)
	if err != nil {
		return nil, nil, err
	}
	if len(merged) == 0 {
		return nil, rest, nil
	}
	names := splitBytes(merged, ',')
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, string(n))
	}
	return out, rest, nil
}

func splitBytes(b []byte, sep byte) [][]byte {
	var out [][]byte
	start := 0
	for i := range b {
		if b[i] == sep {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	out = append(out, b[start:])
	return out
}

// Role selects the transport role.
type Role int

// Transport roles.
const (
	ClientRole Role = iota
	ServerRole
)

func (r Role) String() string {
	if r == ClientRole {
		return "client"
	}
	return "server"
}
