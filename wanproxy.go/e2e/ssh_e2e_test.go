package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	wanproxyssh "github.com/hangtiancheng/wanproxy/wanproxy.go/ssh"
)

const sshConfigTemplate = `{
  "caches": {
    "memory": {
      "type": "memory",
      "size": "128MB"
    }
  },
  "codecs": {
    "encode": {
      "mode": "zlib+xcodec",
      "cache": "memory",
      "compressorLevel": 6,
      "trackStatistics": true
    },
    "decode": {
      "mode": "zlib+xcodec",
      "cache": "memory",
      "compressorLevel": 6,
      "trackStatistics": true
    }
  },
  "proxies": [
    {
      "name": "proxy0",
      "type": "ssh",
      "listen": {
        "host": "127.0.0.1",
        "port": %[1]d
      },
      "upstream": {
        "host": "127.0.0.1",
        "port": %[3]d
      },
      "peerCodec": "encode",
      "hostKey": %[5]q
    },
    {
      "name": "proxy1",
      "type": "ssh",
      "listen": {
        "host": "127.0.0.1",
        "port": %[2]d
      },
      "upstream": {
        "host": "127.0.0.1",
        "port": %[4]d
      },
      "interfaceCodec": "decode",
      "hostKey": %[6]q
    }
  ]
}`

// TestDaemonSSHEndToEnd runs the daemon with two SSH-SSH proxies whose
// WAN hop carries an XCodec+zlib codec: an SSH client connects through
// proxy0, across the codec WAN link to proxy1, and out to an SSH echo
// server.
func TestDaemonSSHEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon e2e in short mode")
	}

	dir := t.TempDir()

	// Host key.
	hostKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	keyPath := filepath.Join(dir, "ssh-server1.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(hostKey)})
	if err := os.WriteFile(keyPath, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	// SSH echo server.
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tr := wanproxyssh.NewTransport(c, wanproxyssh.Config{
					Role:    wanproxyssh.ServerRole,
					HostKey: hostKey,
				})
				c.SetDeadline(time.Now().Add(30 * time.Second))
				if err := tr.Handshake(); err != nil {
					return
				}
				c.SetDeadline(time.Time{})
				io.Copy(tr, tr)
			}(c)
		}
	}()
	echoPort := echoLn.Addr().(*net.TCPAddr).Port

	// Build the daemon.
	bin := filepath.Join(dir, exeName("wanproxy"))
	build := exec.Command("go", "build", "-o", bin, "./cmd/wanproxy")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build daemon: %v\n%s", err, out)
	}

	ports := grabDistinctPorts(t, 2)
	confPath := filepath.Join(dir, "wanproxy.json")
	conf := fmt.Sprintf(sshConfigTemplate, ports[0], ports[1], ports[1], echoPort, keyPath, keyPath)
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	cmd := exec.Command(bin, "-c", confPath)
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	var alive atomic.Bool
	alive.Store(true)
	waitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		alive.Store(false)
		waitCh <- err
	}()
	shutdownDone := false
	defer func() {
		if !shutdownDone {
			cmd.Process.Kill()
			<-waitCh
		}
	}()

	for _, port := range ports {
		if err := waitForPort(port, 5*time.Second, alive.Load); err != nil {
			t.Fatalf("%v\n daemon output:\n%s", err, stderr.String())
		}
	}

	// SSH client through the whole chain.
	clientConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", ports[0]), 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy0: %v", err)
	}
	defer clientConn.Close()
	tr := wanproxyssh.NewTransport(clientConn, wanproxyssh.Config{Role: wanproxyssh.ClientRole})
	clientConn.SetDeadline(time.Now().Add(20 * time.Second))
	if err := tr.Handshake(); err != nil {
		t.Fatalf("client handshake: %v\n daemon output:\n%s", err, stderr.String())
	}
	clientConn.SetDeadline(time.Now().Add(30 * time.Second))

	payload := bytes.Repeat([]byte("SSH WANProxy end-to-end payload! "), 6000)
	var framed []byte
	framed = append(framed, byte(len(payload)>>24), byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload)))
	framed = append(framed, payload...)
	go func() {
		tr.Write(framed)
	}()

	got := make([]byte, len(framed))
	if _, err := io.ReadFull(tr, got); err != nil {
		t.Fatalf("read echo: %v\n daemon output:\n%s", err, stderr.String())
	}
	if !bytes.Equal(got[4:], payload) {
		t.Fatalf("payload mismatch")
	}

	if err := terminate(cmd.Process); err != nil {
		t.Fatalf("signal: %v", err)
	}
	select {
	case err := <-waitCh:
		if !exitOK(err) {
			t.Fatalf("daemon exited with error: %v\n%s", err, stderr.String())
		}
		shutdownDone = true
	case <-time.After(10 * time.Second):
		t.Fatalf("daemon did not exit after SIGTERM\n%s", stderr.String())
	}
}
