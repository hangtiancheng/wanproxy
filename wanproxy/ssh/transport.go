// Package ssh carries a proxy stream over one authenticated SSH channel.
package ssh

import (
	"crypto"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"

	cryptossh "golang.org/x/crypto/ssh"
)

const channelType = "wanproxy-stream"

type Role int

const (
	ClientRole Role = iota
	ServerRole
)

type Config struct {
	Role                   Role
	HostKey                *rsa.PrivateKey
	PeerHostKeyFingerprint string
	Version                string
	Log                    *slog.Logger
}

type Transport struct {
	conn            net.Conn
	cfg             Config
	mu              sync.Mutex
	started, closed bool
	handshakeDone   chan struct{}
	sshConn         cryptossh.Conn
	channel         cryptossh.Channel
	workers         sync.WaitGroup
}

func NewTransport(conn net.Conn, cfg Config) *Transport {
	return &Transport{conn: conn, cfg: cfg, handshakeDone: make(chan struct{})}
}

func PublicKeyFingerprint(key crypto.PublicKey) (string, error) {
	pub, err := cryptossh.NewPublicKey(key)
	if err != nil {
		return "", err
	}
	return cryptossh.FingerprintSHA256(pub), nil
}

func ValidateHostKeyFingerprint(fp string) error {
	if !strings.HasPrefix(fp, "SHA256:") {
		return errors.New("ssh: peer host key SHA256 fingerprint is required")
	}
	data, err := base64.RawStdEncoding.Strict().DecodeString(strings.TrimPrefix(fp, "SHA256:"))
	if err != nil || len(data) != 32 {
		return errors.New("ssh: invalid peer host key SHA256 fingerprint")
	}
	return nil
}

func (t *Transport) Handshake() (err error) {
	t.mu.Lock()
	if t.started || t.closed {
		t.mu.Unlock()
		return errors.New("ssh: transport already started or closed")
	}
	t.started = true
	t.mu.Unlock()
	defer close(t.handshakeDone)
	defer func() {
		if err != nil && t.conn != nil {
			_ = t.conn.Close()
			t.workers.Wait()
			if t.sshConn != nil {
				_ = t.sshConn.Wait()
			}
		}
	}()
	if t.conn == nil {
		return errors.New("ssh: connection is required")
	}

	algorithms := cryptossh.SupportedAlgorithms()
	config := cryptossh.Config{KeyExchanges: algorithms.KeyExchanges, Ciphers: algorithms.Ciphers, MACs: algorithms.MACs}
	var channels <-chan cryptossh.NewChannel
	var requests <-chan *cryptossh.Request
	switch t.cfg.Role {
	case ClientRole:
		if err := ValidateHostKeyFingerprint(t.cfg.PeerHostKeyFingerprint); err != nil {
			return err
		}
		client := &cryptossh.ClientConfig{Config: config, User: "wanproxy", ClientVersion: t.cfg.Version,
			HostKeyAlgorithms: algorithms.HostKeys,
			HostKeyCallback: func(_ string, _ net.Addr, key cryptossh.PublicKey) error {
				got := cryptossh.FingerprintSHA256(key)
				if subtle.ConstantTimeCompare([]byte(got), []byte(t.cfg.PeerHostKeyFingerprint)) != 1 {
					return fmt.Errorf("ssh: peer host key mismatch: got %s", got)
				}
				return nil
			},
		}
		t.sshConn, channels, requests, err = cryptossh.NewClientConn(t.conn, t.conn.RemoteAddr().String(), client)
	case ServerRole:
		if t.cfg.HostKey == nil {
			return errors.New("ssh: server role requires a host key")
		}
		if err := ValidateHostKey(t.cfg.HostKey); err != nil {
			return err
		}
		signer, signErr := cryptossh.NewSignerFromKey(t.cfg.HostKey)
		if signErr != nil {
			return signErr
		}
		server := &cryptossh.ServerConfig{Config: config, NoClientAuth: true, ServerVersion: t.cfg.Version}
		server.AddHostKey(signer)
		var serverConn *cryptossh.ServerConn
		serverConn, channels, requests, err = cryptossh.NewServerConn(t.conn, server)
		if err == nil {
			t.sshConn = serverConn
		}
	default:
		return errors.New("ssh: invalid transport role")
	}
	if err != nil {
		return err
	}
	t.workers.Go(func() { cryptossh.DiscardRequests(requests) })
	var channelRequests <-chan *cryptossh.Request
	if t.cfg.Role == ClientRole {
		t.workers.Go(func() { rejectChannels(channels) })
		t.channel, channelRequests, err = t.sshConn.OpenChannel(channelType, nil)
	} else {
		for incoming := range channels {
			if incoming.ChannelType() != channelType || len(incoming.ExtraData()) != 0 {
				_ = incoming.Reject(cryptossh.UnknownChannelType, "expected wanproxy stream")
				continue
			}
			t.channel, channelRequests, err = incoming.Accept()
			break
		}
		t.workers.Go(func() { rejectChannels(channels) })
	}
	if err != nil {
		return err
	}
	if t.channel == nil {
		return io.ErrUnexpectedEOF
	}
	t.workers.Go(func() { cryptossh.DiscardRequests(channelRequests) })
	if t.cfg.Log != nil {
		t.cfg.Log.Debug("SSH stream channel ready")
	}
	return nil
}

func rejectChannels(channels <-chan cryptossh.NewChannel) {
	for incoming := range channels {
		_ = incoming.Reject(cryptossh.Prohibited, "only one proxy stream is allowed")
	}
}

func (t *Transport) stream() (cryptossh.Channel, error) {
	select {
	case <-t.handshakeDone:
		if t.channel != nil {
			return t.channel, nil
		}
		return nil, errors.New("ssh: handshake failed")
	default:
		return nil, errors.New("ssh: stream used before handshake")
	}
}

func (t *Transport) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	ch, err := t.stream()
	if err != nil {
		return 0, err
	}
	return ch.Read(p)
}

func (t *Transport) Write(p []byte) (int, error) {
	ch, err := t.stream()
	if err != nil {
		return 0, err
	}
	return ch.Write(p)
}

func (t *Transport) CloseWrite() error {
	ch, err := t.stream()
	if err != nil {
		return err
	}
	return ch.CloseWrite()
}

func (t *Transport) Close() error {
	t.mu.Lock()
	t.closed = true
	started := t.started
	t.mu.Unlock()
	var err error
	if t.conn != nil {
		err = t.conn.Close()
	}
	if started {
		<-t.handshakeDone
		t.workers.Wait()
		if t.sshConn != nil {
			_ = t.sshConn.Wait()
		}
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

var _ io.ReadWriteCloser = (*Transport)(nil)
