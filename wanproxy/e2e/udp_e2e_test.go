package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hangtiancheng/wanproxy/wanproxy/proxy"
)

func TestDaemonUDPEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon e2e in short mode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, exeName("wanproxy"))
	build := exec.Command("go", "build", "-o", bin, "./cmd/wanproxy")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	echo, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	if err := echo.(*net.UDPConn).SetWriteBuffer(65535); err != nil {
		t.Fatal(err)
	}
	go func() {
		b := make([]byte, 65535)
		for {
			n, a, e := echo.ReadFrom(b)
			if e != nil {
				return
			}
			if _, e = echo.WriteTo(b[:n], a); e != nil {
				return
			}
		}
	}()
	target := echo.LocalAddr().(*net.UDPAddr)
	data, err := json.Marshal(map[string]any{
		"proxies": []any{map[string]any{"name": "udp-e2e", "type": "udp", "listen": map[string]any{"host": "127.0.0.1", "port": 0}, "upstream": map[string]any{"host": "127.0.0.1", "port": target.Port}, "idleTimeoutSeconds": 1, "maxSessions": 4}},
		"monitor": map[string]any{"listen": map[string]any{"host": "127.0.0.1", "port": 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "wanproxy.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-c", path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			cmd.Process.Kill()
			<-wait
		}
	}()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if address, ok := strings.CutPrefix(scanner.Text(), "monitor listening on "); ok {
				ready <- address
			}
		}
	}()
	var base string
	select {
	case addr := <-ready:
		base = "http://" + addr
	case err := <-wait:
		stopped = true
		t.Fatalf("daemon exited: %v\n%s", err, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("monitor did not become ready")
	}
	httpClient := &http.Client{Timeout: 5 * time.Second}
	status := func() proxy.UDPProxyStatus {
		t.Helper()
		resp, err := httpClient.Get(base + "/status")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var s struct {
			Proxies []proxy.UDPProxyStatus `json:"proxies"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			t.Fatal(err)
		}
		if len(s.Proxies) != 1 || s.Proxies[0].Kind != "udp" {
			t.Fatalf("status: %+v", s)
		}
		return s.Proxies[0]
	}
	bound := status().Address
	if bound == nil {
		t.Fatal("missing UDP bound address")
	}
	c, err := net.Dial("udp", net.JoinHostPort(bound.Address, strconv.Itoa(bound.Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.(*net.UDPConn).SetWriteBuffer(65535); err != nil {
		t.Fatal(err)
	}
	payloads := [][]byte{{}, []byte("UDP daemon"), bytes.Repeat([]byte{42}, 60000)}
	for _, payload := range payloads {
		c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write(payload); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 65535)
		n, err := c.Read(buf)
		if err != nil || !bytes.Equal(buf[:n], payload) {
			t.Fatalf("UDP echo: length=%d error=%v", n, err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for status().PeerToClientDatagrams != 3 {
		if time.Now().After(deadline) {
			t.Fatal("monitor lost UDP counters")
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, err := httpClient.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "udp-e2e") || !strings.Contains(string(body), "datagrams") {
		t.Fatal("monitor HTML missing UDP status")
	}
	if err := terminate(cmd.Process); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-wait:
		stopped = true
		if !exitOK(err) {
			t.Fatalf("daemon exit: %v\n%s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop with active UDP session")
	}
}
