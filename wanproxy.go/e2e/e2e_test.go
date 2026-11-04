// Package e2e runs the JSON-configured wanproxy daemon: a SOCKS client connects
// through the client-side proxy, across an XCodec+zlib WAN link to the
// server-side proxy, through the SOCKS proxy and to the target server.
package e2e

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// terminate stops the daemon. Windows has no SIGTERM, so it kills instead.
func terminate(p *os.Process) error {
	if runtime.GOOS == "windows" {
		return p.Kill()
	}
	return p.Signal(syscall.SIGTERM)
}

// exitOK reports whether the daemon's exit status is acceptable after
// termination. A forced kill on Windows reports a non-nil error.
func exitOK(err error) bool {
	return err == nil || runtime.GOOS == "windows"
}

const configTemplate = `{
  "caches": {
    "memory": {
      "type": "memory",
      "size": "128MB"
    },
    "disk": {
      "type": "disk",
      "size": "1GB",
      "path": %[1]q
    },
    "shared": {
      "type": "pair",
      "primary": "memory",
      "secondary": "disk"
    }
  },
  "codecs": {
    "encode": {
      "mode": "zlib+xcodec",
      "cache": "shared",
      "compressorLevel": 6,
      "trackStatistics": true
    },
    "decode": {
      "mode": "zlib+xcodec",
      "cache": "shared",
      "compressorLevel": 6,
      "trackStatistics": true
    }
  },
  "proxies": [
    {
      "name": "proxy0",
      "type": "tcp",
      "listen": {
        "host": "127.0.0.1",
        "port": %[2]d
      },
      "upstream": {
        "host": "127.0.0.1",
        "port": %[3]d
      },
      "peerCodec": "encode"
    },
    {
      "name": "proxy1",
      "type": "tcp",
      "listen": {
        "host": "127.0.0.1",
        "port": %[3]d
      },
      "upstream": {
        "host": "127.0.0.1",
        "port": %[4]d
      },
      "interfaceCodec": "decode"
    },
    {
      "name": "proxy-socks0",
      "type": "socks",
      "listen": {
        "host": "127.0.0.1",
        "port": %[4]d
      }
    }
  ],
  "monitor": {
    "listen": {
      "host": "127.0.0.1",
      "port": %[5]d
    }
  }
}`

// grabDistinctPorts reserves n distinct ports, holding the listeners
// open until all are reserved so the OS cannot hand one out twice.
func grabDistinctPorts(t *testing.T, n int) []int {
	t.Helper()
	var listeners []net.Listener
	defer func() {
		for _, ln := range listeners {
			ln.Close()
		}
	}()
	seen := map[int]bool{}
	for len(listeners) < n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("grab port: %v", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		if seen[port] {
			ln.Close()
			continue
		}
		seen[port] = true
		listeners = append(listeners, ln)
	}
	ports := make([]int, n)
	for i, ln := range listeners {
		ports[i] = ln.Addr().(*net.TCPAddr).Port
	}
	return ports
}

func waitForPort(port int, timeout time.Duration, alive func() bool) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		if !alive() {
			return fmt.Errorf("daemon exited before binding port %d", port)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("port %d did not open within %v", port, timeout)
}

func TestDaemonEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon e2e in short mode")
	}

	dir := t.TempDir()

	// Build the daemon.
	bin := filepath.Join(dir, exeName("wanproxy"))
	build := exec.Command("go", "build", "-o", bin, "./cmd/wanproxy")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build daemon: %v\n%s", err, out)
	}

	cacheDir := filepath.Join(dir, "wanproxy.xcache")
	confPath := filepath.Join(dir, "wanproxy.json")

	// Start the daemon, retrying with fresh ports if a grabbed port is
	// taken again before the daemon can bind it.
	var cmd *exec.Cmd
	var stderr bytes.Buffer
	var ports []int
	var waitCh chan error
	var alive atomic.Bool

	for attempt := 0; attempt < 3 && waitCh == nil; attempt++ {
		ports = grabDistinctPorts(t, 4)
		conf := fmt.Sprintf(configTemplate, cacheDir, ports[0], ports[1], ports[2], ports[3])
		if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
			t.Fatal(err)
		}
		stderr.Reset()
		cmd = exec.Command(bin, "-c", confPath)
		cmd.Stderr = &stderr
		cmd.Stdout = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start daemon: %v", err)
		}
		alive.Store(true)
		ch := make(chan error, 1)
		go func() {
			err := cmd.Wait()
			alive.Store(false)
			ch <- err
		}()

		ok := true
		for _, port := range ports {
			if err := waitForPort(port, 5*time.Second, alive.Load); err != nil {
				t.Logf("attempt %d: %v; daemon output:\n%s", attempt, err, stderr.String())
				ok = false
				break
			}
		}
		if ok {
			waitCh = ch
			break
		}
		cmd.Process.Kill()
		<-ch
	}
	if waitCh == nil {
		t.Fatalf("daemon did not start after 3 attempts; last output:\n%s", stderr.String())
	}

	shutdownDone := false
	defer func() {
		if !shutdownDone {
			cmd.Process.Kill()
			<-waitCh
		}
	}()

	// Echo server as the final target.
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
				io.Copy(c, c)
				if tc, ok := c.(*net.TCPConn); ok {
					tc.CloseWrite()
				}
			}(c)
		}
	}()
	echoAddr := echoLn.Addr().(*net.TCPAddr)

	// SOCKS5 CONNECT through the whole chain.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", ports[0]), 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy0: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(greeting, []byte{0x05, 0x00}) {
		t.Fatalf("socks greeting = % x", greeting)
	}

	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, echoAddr.IP.To4()...)
	var pb [2]byte
	binary.BigEndian.PutUint16(pb[:], uint16(echoAddr.Port))
	req = append(req, pb[:]...)
	if _, err := conn.Write(req); err != nil {
		t.Fatal(err)
	}
	resp := make([]byte, 10)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatal(err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		t.Fatalf("socks response = % x", resp)
	}

	payload := bytes.Repeat([]byte("WANProxy end-to-end test payload! "), 4096)
	go func() {
		conn.Write(payload)
		conn.(*net.TCPConn).CloseWrite()
	}()
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %d bytes, want %d", len(got), len(payload))
	}

	// Monitor status must show the deduplication: proxy0 pushed far
	// fewer wire bytes toward the WAN than the plaintext volume.
	statusURL := fmt.Sprintf("http://127.0.0.1:%d/status", ports[3])
	httpResp, err := http.Get(statusURL)
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != 200 {
		t.Fatalf("monitor status = %d", httpResp.StatusCode)
	}
	var status struct {
		Proxies []struct {
			Name      string `json:"name"`
			PeerCodec *struct {
				Stats struct {
					IncomingToCodec int64 `json:"incomingToCodecBytes"`
					CodecToOutgoing int64 `json:"codecToOutgoingBytes"`
				} `json:"stats"`
			} `json:"peerCodec"`
		} `json:"proxies"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&status); err != nil {
		t.Fatalf("monitor json: %v", err)
	}
	if len(status.Proxies) != 3 {
		t.Fatalf("monitor proxies = %d, want 3", len(status.Proxies))
	}
	var incoming, outgoing int64 = -1, -1
	for _, p := range status.Proxies {
		if p.Name == "proxy0" && p.PeerCodec != nil {
			incoming = p.PeerCodec.Stats.IncomingToCodec
			outgoing = p.PeerCodec.Stats.CodecToOutgoing
		}
	}
	if incoming < int64(len(payload)) {
		t.Fatalf("incoming_to_codec = %d, want >= %d", incoming, len(payload))
	}
	if outgoing < 0 || outgoing >= int64(len(payload))/10 {
		t.Fatalf("codec_to_outgoing = %d, want heavy deduplication of %d payload bytes", outgoing, len(payload))
	}

	// Graceful shutdown on SIGTERM (forceful on Windows).
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
