package ssh

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"sync"
)

const maxPacketSize = 4 * 1024 * 1024

const maxVersionLines = 255

type Config struct {
	Role Role

	HostKey *rsa.PrivateKey

	Tagged bool

	Version string

	Log *slog.Logger
}

type Transport struct {
	conn net.Conn
	cfg  Config
	log  *slog.Logger

	clientVersion []byte
	serverVersion []byte
	clientKexinit []byte
	serverKexinit []byte
	kexAccum      []byte

	kex     keyExchangeAlgorithm
	hostKey hostKeyAlgorithm
	encC2S  encryptionAlgorithm
	encS2C  encryptionAlgorithm
	macC2S  macAlgorithm
	macS2C  macAlgorithm

	sharedSecret []byte
	exchangeHash []byte
	sessionID    []byte

	sendEnc    cipherState
	sendEncAlg encryptionAlgorithm
	sendMACAlg macAlgorithm
	sendMACKey []byte
	sendSeq    uint32

	recvEnc       cipherState
	recvEncAlg    encryptionAlgorithm
	recvMACAlg    macAlgorithm
	recvMACKey    []byte
	recvSeq       uint32
	recvActivated bool

	wireBuf    []byte
	firstBlock []byte
	plainBuf   []byte
	writeBuf   []byte
	wireErr    error

	wmu           sync.Mutex
	handshakeDone bool
}

func NewTransport(conn net.Conn, cfg Config) *Transport {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &Transport{
		conn: conn,
		cfg:  cfg,
		log:  log.With("component", "/ssh/transport/"+cfg.Role.String()),
	}
}

func (t *Transport) Handshake() error {
	if t.cfg.Role == ServerRole && t.cfg.HostKey == nil {
		return errors.New("ssh: server role requires a host key")
	}

	version := t.cfg.Version
	if version == "" {
		version = "SSH-2.0-WANProxy Go"
	}
	if err := t.writeAll([]byte(version + "\r\n")); err != nil {
		return err
	}
	if t.cfg.Role == ClientRole {
		t.clientVersion = []byte(version)
	} else {
		t.serverVersion = []byte(version)
	}
	peerVersion, err := t.readVersionLine()
	if err != nil {
		return err
	}
	if t.cfg.Role == ClientRole {
		t.serverVersion = peerVersion
	} else {
		t.clientVersion = peerVersion
	}

	kexinit := t.buildKexinit()
	if err := t.sendPacket(kexinit); err != nil {
		return err
	}
	if t.cfg.Role == ClientRole {
		t.clientKexinit = kexinit
	} else {
		t.serverKexinit = kexinit
	}

	payload, err := t.readPacket()
	if err != nil {
		return err
	}
	if len(payload) == 0 || payload[0] != MsgKexInit {
		return fmt.Errorf("ssh: expected KEXINIT, got message %d", firstByte(payload))
	}
	if t.cfg.Role == ClientRole {
		t.serverKexinit = payload
	} else {
		t.clientKexinit = payload
	}
	if err := t.negotiate(payload); err != nil {
		return err
	}

	if err := t.runKeyExchange(); err != nil {
		return err
	}

	if err := t.sendPacket([]byte{MsgNewKeys}); err != nil {
		return err
	}
	if err := t.activateSend(); err != nil {
		return err
	}
	for !t.recvActivated {
		payload, err := t.readPacket()
		if err != nil {
			return err
		}
		switch payload[0] {
		case MsgNewKeys:
			if err := t.activateRecv(); err != nil {
				return err
			}
		case MsgDisconnect:
			return errors.New("ssh: peer disconnected during handshake")
		case MsgIgnore:
		default:
			if err := t.deliverApp(payload); err != nil {
				return err
			}
		}
	}

	t.handshakeDone = true
	t.log.Debug("ssh handshake complete",
		"kex", t.kex.name(),
		"peerVersion", string(peerVersion))
	return nil
}

func firstByte(p []byte) byte {
	if len(p) == 0 {
		return 0
	}
	return p[0]
}

func (t *Transport) readVersionLine() ([]byte, error) {
	var line []byte
	lines := 0
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(t.conn, one); err != nil {
			return nil, fmt.Errorf("ssh: reading version string: %w", err)
		}
		if one[0] != '\n' {
			line = append(line, one[0])
			if len(line) > 8192 {
				return nil, errors.New("ssh: version line too long")
			}
			continue
		}
		trimmed := bytes.TrimRight(line, "\r")
		line = line[:0]
		if bytes.HasPrefix(trimmed, []byte("SSH-")) {
			if !bytes.HasPrefix(trimmed, []byte("SSH-2.0")) {
				return nil, fmt.Errorf("ssh: unsupported version %q", trimmed)
			}
			return trimmed, nil
		}
		lines++
		if lines > maxVersionLines {
			return nil, errors.New("ssh: too many lines before version string")
		}
	}
}

func (t *Transport) buildKexinit() []byte {
	cookie := make([]byte, 16)
	if _, err := rand.Read(cookie); err != nil {
		panic("ssh: could not generate KEXINIT cookie: " + err.Error())
	}

	kexNames := make([]string, 0)
	for _, alg := range keyExchangeAlgorithms() {
		kexNames = append(kexNames, alg.name())
	}
	encNames := make([]string, 0)
	for _, alg := range encryptionAlgorithms() {
		encNames = append(encNames, alg.name())
	}
	macNames := make([]string, 0)
	for _, alg := range macAlgorithms() {
		macNames = append(macNames, alg.name())
	}

	var out []byte
	out = append(out, MsgKexInit)
	out = append(out, cookie...)
	out = appendNameList(out, kexNames)
	out = appendNameList(out, []string{"ssh-rsa"})
	out = appendNameList(out, encNames)
	out = appendNameList(out, encNames)
	out = appendNameList(out, macNames)
	out = appendNameList(out, macNames)
	out = appendNameList(out, []string{"none"})
	out = appendNameList(out, []string{"none"})
	out = appendNameList(out, nil)
	out = appendNameList(out, nil)
	out = append(out, 0)
	out = appendUint32(out, 0)
	return out
}

func (t *Transport) negotiate(peerKexinit []byte) error {
	if len(peerKexinit) < 17 {
		return errMalformed
	}
	in := peerKexinit[17:]

	read := func(what string) ([]string, []byte, error) {
		names, rest, err := readNameList(in)
		if err != nil {
			return nil, nil, fmt.Errorf("ssh: decoding %s name-list: %w", what, err)
		}
		in = rest
		return names, rest, nil
	}

	peerKex, _, err := read("key exchange")
	if err != nil {
		return err
	}
	peerHostKey, _, err := read("server host key")
	if err != nil {
		return err
	}
	peerEncC2S, _, err := read("encryption (client->server)")
	if err != nil {
		return err
	}
	peerEncS2C, _, err := read("encryption (server->client)")
	if err != nil {
		return err
	}
	peerMacC2S, _, err := read("MAC (client->server)")
	if err != nil {
		return err
	}
	peerMacS2C, _, err := read("MAC (server->client)")
	if err != nil {
		return err
	}
	peerCompC2S, _, err := read("compression (client->server)")
	if err != nil {
		return err
	}
	peerCompS2C, _, err := read("compression (server->client)")
	if err != nil {
		return err
	}
	if _, _, err := read("languages (client->server)"); err != nil {
		return err
	}
	if _, _, err := read("languages (server->client)"); err != nil {
		return err
	}

	localKex := make([]string, 0)
	for _, alg := range keyExchangeAlgorithms() {
		localKex = append(localKex, alg.name())
	}
	localEnc := make([]string, 0)
	for _, alg := range encryptionAlgorithms() {
		localEnc = append(localEnc, alg.name())
	}
	localMac := make([]string, 0)
	for _, alg := range macAlgorithms() {
		localMac = append(localMac, alg.name())
	}
	localComp := []string{"none"}

	choose := func(local, peer []string, what string) (string, error) {
		var clientList, serverList []string
		if t.cfg.Role == ClientRole {
			clientList, serverList = local, peer
		} else {
			clientList, serverList = peer, local
		}
		if len(clientList) == 0 && len(serverList) == 0 {
			return "", nil
		}
		for _, name := range clientList {
			for _, candidate := range serverList {
				if name == candidate {
					for _, l := range local {
						if l == name {
							t.log.Debug("selected algorithm", "type", what, "algorithm", name)
							return name, nil
						}
					}
				}
			}
		}
		return "", fmt.Errorf("ssh: failed to choose %s algorithm", what)
	}

	kexName, err := choose(localKex, peerKex, "key exchange")
	if err != nil {
		return err
	}
	t.kex = findKeyExchange(kexName)
	if t.kex == nil {
		return fmt.Errorf("ssh: unsupported key exchange %q", kexName)
	}

	hostKeyName, err := choose([]string{"ssh-rsa"}, peerHostKey, "server host key")
	if err != nil {
		return err
	}
	if hostKeyName != "" {
		t.hostKey = findHostKey(hostKeyName, t.cfg.Role, t.cfg.HostKey)
		if t.hostKey == nil {
			return fmt.Errorf("ssh: unsupported host key %q", hostKeyName)
		}
	}

	encC2S, err := choose(localEnc, peerEncC2S, "encryption (client->server)")
	if err != nil {
		return err
	}
	encS2C, err := choose(localEnc, peerEncS2C, "encryption (server->client)")
	if err != nil {
		return err
	}
	t.encC2S = findEncryption(encC2S)
	t.encS2C = findEncryption(encS2C)
	if t.encC2S == nil || t.encS2C == nil {
		return errors.New("ssh: unsupported negotiated encryption")
	}

	macC2S, err := choose(localMac, peerMacC2S, "MAC (client->server)")
	if err != nil {
		return err
	}
	macS2C, err := choose(localMac, peerMacS2C, "MAC (server->client)")
	if err != nil {
		return err
	}
	t.macC2S = findMAC(macC2S)
	t.macS2C = findMAC(macS2C)
	if t.macC2S == nil || t.macS2C == nil {
		return errors.New("ssh: unsupported negotiated MAC")
	}

	compC2S, err := choose(localComp, peerCompC2S, "compression (client->server)")
	if err != nil {
		return err
	}
	compS2C, err := choose(localComp, peerCompS2C, "compression (server->client)")
	if err != nil {
		return err
	}
	if compC2S != "" && compC2S != "none" {
		return fmt.Errorf("ssh: unsupported compression %q", compC2S)
	}
	if compS2C != "" && compS2C != "none" {
		return fmt.Errorf("ssh: unsupported compression %q", compS2C)
	}

	return nil
}

func (t *Transport) runKeyExchange() error {
	dh := &dhState{}
	if t.cfg.Role == ClientRole {
		return t.clientKeyExchange(dh)
	}
	return t.serverKeyExchange(dh)
}

func (t *Transport) clientKeyExchange(dh *dhState) error {
	var req []byte
	req = appendUint32(req, dhGroupMin)
	req = appendUint32(req, dhGroupMin)
	req = appendUint32(req, dhGroupMax)
	t.kexAccum = append(t.kexAccum, req...)
	if err := t.sendPacket(append([]byte{kexDHGroupExchangeRequest}, req...)); err != nil {
		return err
	}

	payload, err := t.readPacket()
	if err != nil {
		return err
	}
	if len(payload) == 0 || payload[0] != kexDHGroupExchangeGroup {
		return fmt.Errorf("ssh: expected DH group, got message %d", firstByte(payload))
	}
	body := payload[1:]
	t.kexAccum = append(t.kexAccum, body...)
	p, rest, err := readMPInt(body)
	if err != nil {
		return err
	}
	g, rest, err := readMPInt(rest)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errMalformed
	}
	if p.BitLen() < dhGroupMin || p.BitLen() > dhGroupMax {
		return fmt.Errorf("ssh: DH prime size out of range (%d bits)", p.BitLen())
	}
	if g.Cmp(big.NewInt(1)) <= 0 || g.Cmp(new(big.Int).Sub(p, big.NewInt(1))) >= 0 {
		return errors.New("ssh: DH generator out of range")
	}
	dh.p, dh.g = p, g
	if err := dh.generate(); err != nil {
		return err
	}

	var init []byte
	init = appendMPInt(init, dh.pub)
	t.kexAccum = append(t.kexAccum, init...)
	if err := t.sendPacket(append([]byte{kexDHGroupExchangeInitialize}, init...)); err != nil {
		return err
	}

	payload, err = t.readPacket()
	if err != nil {
		return err
	}
	if len(payload) == 0 || payload[0] != kexDHGroupExchangeReply {
		return fmt.Errorf("ssh: expected DH reply, got message %d", firstByte(payload))
	}
	body = payload[1:]
	serverHostKeyBlob, rest, err := readString(body)
	if err != nil {
		return err
	}
	f, rest, err := readMPInt(rest)
	if err != nil {
		return err
	}
	signature, rest, err := readString(rest)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errMalformed
	}

	if err := t.hostKey.decodePublicKey(serverHostKeyBlob); err != nil {
		return fmt.Errorf("ssh: could not decode server public key: %w", err)
	}

	var fWire []byte
	fWire = appendMPInt(fWire, f)
	t.kexAccum = append(t.kexAccum, fWire...)

	if err := dh.computeShared(f); err != nil {
		return err
	}
	t.finishKeyExchange(dh, serverHostKeyBlob)

	if err := t.hostKey.verify(signature, t.exchangeHash); err != nil {
		return fmt.Errorf("ssh: failed to verify exchange hash: %w", err)
	}
	return nil
}

func (t *Transport) serverKeyExchange(dh *dhState) error {
	payload, err := t.readPacket()
	if err != nil {
		return err
	}
	if len(payload) == 0 || payload[0] != kexDHGroupExchangeRequest {
		return fmt.Errorf("ssh: expected DH request, got message %d", firstByte(payload))
	}
	body := payload[1:]
	t.kexAccum = append(t.kexAccum, body...)
	min, rest, err := readUint32(body)
	if err != nil {
		return err
	}
	n, rest, err := readUint32(rest)
	if err != nil {
		return err
	}
	max, _, err := readUint32(rest)
	if err != nil {
		return err
	}
	if min < dhGroupMin {
		min = dhGroupMin
	}
	if max > dhGroupMax {
		max = dhGroupMax
	}
	if min > max {
		return errors.New("ssh: DH group request min > max")
	}
	if n < min {
		n = min
	} else if n > max {
		n = max
	}
	_ = n

	group := testPrimeAndGenerator
	t.kexAccum = append(t.kexAccum, group...)
	if err := t.sendPacket(append([]byte{kexDHGroupExchangeGroup}, group...)); err != nil {
		return err
	}
	p, rest2, err := readMPInt(group)
	if err != nil {
		return err
	}
	g, _, err := readMPInt(rest2)
	if err != nil {
		return err
	}
	dh.p, dh.g = p, g

	payload, err = t.readPacket()
	if err != nil {
		return err
	}
	if len(payload) == 0 || payload[0] != kexDHGroupExchangeInitialize {
		return fmt.Errorf("ssh: expected DH initialize, got message %d", firstByte(payload))
	}
	body = payload[1:]
	t.kexAccum = append(t.kexAccum, body...)
	e, _, err := readMPInt(body)
	if err != nil {
		return err
	}

	if err := dh.generate(); err != nil {
		return err
	}
	var fWire []byte
	fWire = appendMPInt(fWire, dh.pub)
	t.kexAccum = append(t.kexAccum, fWire...)

	if err := dh.computeShared(e); err != nil {
		return err
	}
	serverHostKeyBlob := t.hostKey.encodePublicKey()
	t.finishKeyExchange(dh, serverHostKeyBlob)

	signature, err := t.hostKey.sign(t.exchangeHash)
	if err != nil {
		return err
	}

	var reply []byte
	reply = appendString(reply, serverHostKeyBlob)
	reply = appendMPInt(reply, dh.pub)
	reply = appendString(reply, signature)
	return t.sendPacket(append([]byte{kexDHGroupExchangeReply}, reply...))
}

func (t *Transport) finishKeyExchange(dh *dhState, serverHostKeyBlob []byte) {
	t.exchangeHash = exchangeHash(
		t.kex.hashFunc(),
		t.clientVersion, t.serverVersion,
		t.clientKexinit, t.serverKexinit,
		serverHostKeyBlob, t.kexAccum, dh.shared)
	if t.sessionID == nil {
		t.sessionID = t.exchangeHash
	}
	var k []byte
	k = appendMPInt(k, dh.shared)
	t.sharedSecret = k
}

func (t *Transport) activateSend() error {
	var ivLetter, keyLetter, macLetter byte
	var encAlg encryptionAlgorithm
	var macAlg macAlgorithm
	if t.cfg.Role == ClientRole {
		ivLetter, keyLetter, macLetter = 'A', 'C', 'E'
		encAlg, macAlg = t.encC2S, t.macC2S
	} else {
		ivLetter, keyLetter, macLetter = 'B', 'D', 'F'
		encAlg, macAlg = t.encS2C, t.macS2C
	}
	if encAlg != nil {
		iv := deriveKey(t.kex.hashFunc(), t.sharedSecret, t.exchangeHash, t.sessionID, ivLetter, encAlg.ivSize())
		key := deriveKey(t.kex.hashFunc(), t.sharedSecret, t.exchangeHash, t.sessionID, keyLetter, encAlg.keySize())
		state, err := encAlg.newEncrypt(key, iv)
		if err != nil {
			return fmt.Errorf("ssh: failed to initialize local-to-remote encryption: %w", err)
		}
		t.sendEnc, t.sendEncAlg = state, encAlg
	}
	if macAlg != nil {
		t.sendMACKey = deriveKey(t.kex.hashFunc(), t.sharedSecret, t.exchangeHash, t.sessionID, macLetter, macAlg.keySize())
		t.sendMACAlg = macAlg
	}
	return nil
}

func (t *Transport) activateRecv() error {
	var ivLetter, keyLetter, macLetter byte
	var encAlg encryptionAlgorithm
	var macAlg macAlgorithm
	if t.cfg.Role == ClientRole {
		ivLetter, keyLetter, macLetter = 'B', 'D', 'F'
		encAlg, macAlg = t.encS2C, t.macS2C
	} else {
		ivLetter, keyLetter, macLetter = 'A', 'C', 'E'
		encAlg, macAlg = t.encC2S, t.macC2S
	}
	if encAlg != nil {
		iv := deriveKey(t.kex.hashFunc(), t.sharedSecret, t.exchangeHash, t.sessionID, ivLetter, encAlg.ivSize())
		key := deriveKey(t.kex.hashFunc(), t.sharedSecret, t.exchangeHash, t.sessionID, keyLetter, encAlg.keySize())
		state, err := encAlg.newDecrypt(key, iv)
		if err != nil {
			return fmt.Errorf("ssh: failed to initialize remote-to-local encryption: %w", err)
		}
		t.recvEnc, t.recvEncAlg = state, encAlg
	}
	if macAlg != nil {
		t.recvMACKey = deriveKey(t.kex.hashFunc(), t.sharedSecret, t.exchangeHash, t.sessionID, macLetter, macAlg.keySize())
		t.recvMACAlg = macAlg
	}
	t.recvActivated = true
	return nil
}

func (t *Transport) writeAll(p []byte) error {
	for len(p) > 0 {
		n, err := t.conn.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

func (t *Transport) sendPacket(payload []byte) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	return t.sendPacketLocked(payload)
}

func (t *Transport) sendPacketLocked(payload []byte) error {
	blockSize := packetBlockSize(t.sendEncAlg)
	packetLen := 1 + len(payload)
	paddingLen := 4 + (blockSize - ((4 + packetLen + 4) % blockSize))
	packetLen += paddingLen

	packet := make([]byte, 4+packetLen)
	binary.BigEndian.PutUint32(packet[:4], uint32(packetLen))
	packet[4] = byte(paddingLen)
	copy(packet[5:], payload)
	if _, err := rand.Read(packet[5+len(payload):]); err != nil {
		return err
	}

	var macBytes []byte
	if t.sendMACAlg != nil {
		mac := t.sendMACAlg.newMAC(t.sendMACKey)
		var seqBuf [4]byte
		binary.BigEndian.PutUint32(seqBuf[:], t.sendSeq)
		mac.Write(seqBuf[:])
		mac.Write(packet)
		macBytes = mac.Sum(nil)[:t.sendMACAlg.size()]
	}

	if t.sendEnc != nil {
		t.sendEnc.crypt(packet, packet)
	}

	if len(macBytes) > 0 {
		packet = append(packet, macBytes...)
	}
	t.sendSeq++
	return t.writeAll(packet)
}

func (t *Transport) fillWire() error {
	if t.wireErr != nil {
		return t.wireErr
	}
	buf := make([]byte, 32768)
	n, err := t.conn.Read(buf)
	if n > 0 {
		t.wireBuf = append(t.wireBuf, buf[:n]...)
		t.wireErr = err
		return nil
	}
	if err != nil {
		return err
	}
	return nil
}

func (t *Transport) readPacket() ([]byte, error) {
	for {
		blockSize := packetBlockSize(t.recvEncAlg)
		macSize := 0
		if t.recvMACAlg != nil {
			macSize = t.recvMACAlg.size()
		}

		if len(t.firstBlock) == 0 {
			if len(t.wireBuf) < blockSize {
				if err := t.fillWire(); err != nil {
					return nil, err
				}
				continue
			}
			block := t.wireBuf[:blockSize]
			t.wireBuf = t.wireBuf[blockSize:]
			if t.recvEnc != nil {
				dec := make([]byte, blockSize)
				t.recvEnc.crypt(dec, block)
				block = dec
			}
			t.firstBlock = block
		}
		if len(t.firstBlock) < 4 {
			return nil, errMalformed
		}
		packetLen := binary.BigEndian.Uint32(t.firstBlock[:4])
		if packetLen < 12 || (uint64(packetLen)+4)%uint64(blockSize) != 0 {
			return nil, errors.New("ssh: invalid packet length or block alignment")
		}
		if packetLen > maxPacketSize {
			return nil, fmt.Errorf("ssh: packet too large (%d bytes)", packetLen)
		}

		total := 4 + int(packetLen)
		if blockSize+len(t.wireBuf) < total+macSize {
			if err := t.fillWire(); err != nil {
				return nil, err
			}
			continue
		}

		packet := make([]byte, 0, total)
		packet = append(packet, t.firstBlock...)
		t.firstBlock = nil
		if total > blockSize {
			rest := t.wireBuf[:total-blockSize]
			t.wireBuf = t.wireBuf[total-blockSize:]
			if t.recvEnc != nil {
				dec := make([]byte, len(rest))
				t.recvEnc.crypt(dec, rest)
				rest = dec
			}
			packet = append(packet, rest...)
		}

		if macSize > 0 {
			macBytes := t.wireBuf[:macSize]
			t.wireBuf = t.wireBuf[macSize:]
			mac := t.recvMACAlg.newMAC(t.recvMACKey)
			var seqBuf [4]byte
			binary.BigEndian.PutUint32(seqBuf[:], t.recvSeq)
			mac.Write(seqBuf[:])
			mac.Write(packet)
			expected := mac.Sum(nil)[:macSize]
			if !hmac.Equal(expected, macBytes) {
				return nil, errors.New("ssh: received MAC does not match expected MAC")
			}
		}
		t.recvSeq++

		paddingLen := int(packet[4])
		if paddingLen < 4 || paddingLen > len(packet)-5 {
			return nil, errors.New("ssh: padding too large for packet")
		}
		payload := packet[5 : len(packet)-paddingLen]
		if len(payload) == 0 {
			return nil, errors.New("ssh: empty packet")
		}
		return payload, nil
	}
}

func (t *Transport) deliverApp(payload []byte) error {
	if t.cfg.Tagged {
		if len(payload) < 2 || payload[0] != appPacketTag {
			return errors.New("ssh: got encoded packet with wrong message")
		}
		t.plainBuf = append(t.plainBuf, payload[1:]...)
		return nil
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(payload)))
	t.plainBuf = append(t.plainBuf, lenBuf[:]...)
	t.plainBuf = append(t.plainBuf, payload...)
	return nil
}

func (t *Transport) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(t.plainBuf) == 0 {
		payload, err := t.readPacket()
		if err != nil {
			return 0, err
		}
		switch payload[0] {
		case MsgDisconnect:
			return 0, io.EOF
		case MsgIgnore:
			continue
		default:
			if err := t.deliverApp(payload); err != nil {
				return 0, err
			}
		}
	}
	n := copy(p, t.plainBuf)
	t.plainBuf = t.plainBuf[n:]
	return n, nil
}

func (t *Transport) Write(p []byte) (int, error) {
	if !t.handshakeDone {
		return 0, errors.New("ssh: write before handshake")
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()

	if t.cfg.Tagged {
		written := 0
		for len(p) > 0 {
			size := min(len(p), 32768)
			payload := make([]byte, 1, 1+size)
			payload[0] = appPacketTag
			payload = append(payload, p[:size]...)
			if err := t.sendPacketLocked(payload); err != nil {
				return written, err
			}
			written += size
			p = p[size:]
		}
		return written, nil
	}

	t.writeBuf = append(t.writeBuf, p...)
	for len(t.writeBuf) >= 4 {
		frameLen := binary.BigEndian.Uint32(t.writeBuf[:4])
		if frameLen == 0 || frameLen > maxPacketSize-256 {
			return 0, errors.New("ssh: invalid application frame length")
		}
		if uint64(frameLen) > uint64(len(t.writeBuf)-4) {
			break
		}
		if frameLen > 0 {
			if err := t.sendPacketLocked(t.writeBuf[4 : 4+frameLen]); err != nil {
				return 0, err
			}
		}
		t.writeBuf = t.writeBuf[4+frameLen:]
	}
	return len(p), nil
}

func (t *Transport) CloseWrite() error {
	if hc, ok := t.conn.(interface{ CloseWrite() error }); ok {
		return hc.CloseWrite()
	}
	return nil
}

func (t *Transport) Close() error { return t.conn.Close() }

var _ io.ReadWriteCloser = (*Transport)(nil)
var _ = big.NewInt
