package ssh

import (
	"bytes"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"testing"
)

func TestPacketRejectsCBCMisalignment(t *testing.T) {
	alg := findEncryption("aes128-cbc")
	dec, err := alg.newDecrypt(make([]byte, 16), make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	tr := &Transport{recvEncAlg: alg, recvEnc: dec, firstBlock: make([]byte, 16), wireBuf: make([]byte, 5)}
	binary.BigEndian.PutUint32(tr.firstBlock, 17)
	if _, err := tr.readPacket(); err == nil {
		t.Fatal("accepted misaligned CBC packet")
	}
}

func TestDHRejectsDegeneratePublicValues(t *testing.T) {
	for _, value := range []int64{0, 1, 22, 23} {
		dh := &dhState{p: big.NewInt(23), x: big.NewInt(3)}
		if err := dh.computeShared(big.NewInt(value)); err == nil {
			t.Fatalf("accepted public value %d", value)
		}
	}
}

type finalReadConn struct {
	net.Conn
	data []byte
}

func (c *finalReadConn) Read(p []byte) (int, error) {
	n := copy(p, c.data)
	c.data = c.data[n:]
	return n, io.EOF
}

func TestPacketPreservesBytesWithEOF(t *testing.T) {
	packet := make([]byte, 16)
	binary.BigEndian.PutUint32(packet, 12)
	packet[4] = 4
	copy(packet[5:], "payload")
	tr := NewTransport(&finalReadConn{data: packet}, Config{})
	got, err := tr.readPacket()
	if err != nil || !bytes.Equal(got, []byte("payload")) {
		t.Fatalf("final packet lost: %q, %v", got, err)
	}
}

func TestUntaggedWriteRejectsOversizedHeader(t *testing.T) {
	tr := NewTransport(nil, Config{})
	tr.handshakeDone = true
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], maxPacketSize+1)
	if _, err := tr.Write(header[:]); err == nil {
		t.Fatal("accepted an unbounded application frame")
	}
}
