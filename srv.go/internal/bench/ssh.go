package bench

import (
	"crypto/rsa"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/hangtiancheng/wanproxy/srv.go/internal/payload"
	wanproxyssh "github.com/hangtiancheng/wanproxy/wanproxy.go/ssh"
)

func benchSSH(proxyAddr string, pattern payload.Pattern, size, seed int64) (string, error) {
	if proxyAddr == "" {
		return "", fmt.Errorf("ssh scenario requires a proxy")
	}

	conn, err := net.DialTimeout("tcp", proxyAddr, 10*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	tr := wanproxyssh.NewTransport(conn, wanproxyssh.Config{Role: wanproxyssh.ClientRole})
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := tr.Handshake(); err != nil {
		return "", fmt.Errorf("ssh handshake: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))

	const frameSize = 1 << 20
	h := sha256.New()
	echoErr := make(chan error, 1)
	go func() {
		remaining := size
		var header [4]byte
		buf := make([]byte, 64*1024)
		for remaining > 0 {
			if _, err := io.ReadFull(tr, header[:]); err != nil {
				echoErr <- fmt.Errorf("echo frame header: %w", err)
				return
			}
			frameLen := int64(binary.BigEndian.Uint32(header[:]))
			if frameLen <= 0 || frameLen > remaining {
				echoErr <- fmt.Errorf("bad echo frame length %d (remaining %d)", frameLen, remaining)
				return
			}
			done := int64(0)
			for done < frameLen {
				n, err := io.ReadFull(tr, buf[:min(frameLen-done, int64(len(buf)))])
				if n > 0 {
					h.Write(buf[:n])
					done += int64(n)
				}
				if err != nil {
					echoErr <- fmt.Errorf("echo frame body: %w", err)
					return
				}
			}
			remaining -= frameLen
		}
		echoErr <- nil
	}()

	pr := payload.NewReader(pattern, size, seed)
	buf := make([]byte, frameSize)
	var header [4]byte
	written := int64(0)
	for written < size {
		n, err := io.ReadFull(pr, buf[:min(frameSize, size-written)])
		if n > 0 {
			binary.BigEndian.PutUint32(header[:], uint32(n))
			if _, werr := tr.Write(header[:]); werr != nil {
				return "", fmt.Errorf("ssh write header: %w", werr)
			}
			if _, werr := tr.Write(buf[:n]); werr != nil {
				return "", fmt.Errorf("ssh write: %w", werr)
			}
			written += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}

	if err := <-echoErr; err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

type SSHEchoServer struct {
	ln      net.Listener
	hostKey *rsa.PrivateKey

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

func ServeSSHEcho(listen, hostkeyPath string) (*SSHEchoServer, error) {
	pemData, err := os.ReadFile(hostkeyPath)
	if err != nil {
		return nil, fmt.Errorf("read hostkey: %w", err)
	}
	hostKey, err := wanproxyssh.LoadRSAPrivateKeyPEM(pemData)
	if err != nil {
		return nil, fmt.Errorf("parse hostkey: %w", err)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	s := &SSHEchoServer{ln: ln, hostKey: hostKey, conns: make(map[net.Conn]struct{})}
	go s.acceptLoop()
	return s, nil
}

func (s *SSHEchoServer) Addr() string { return s.ln.Addr().String() }

func (s *SSHEchoServer) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		go s.handle(conn)
	}
}

func (s *SSHEchoServer) handle(conn net.Conn) {
	defer func() {
		conn.Close()
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()
	tr := wanproxyssh.NewTransport(conn, wanproxyssh.Config{
		Role:    wanproxyssh.ServerRole,
		HostKey: s.hostKey,
	})
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := tr.Handshake(); err != nil {
		log.Printf("[ssh-echo] handshake: %v", err)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	if _, err := io.Copy(tr, tr); err != nil {
		log.Printf("[ssh-echo] copy: %v", err)
	}
}

func (s *SSHEchoServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	err := s.ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	return err
}
