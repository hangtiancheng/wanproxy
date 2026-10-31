// Package daemon orchestrates
// the demo infrastructure: builds the wanproxy.go binaries on demand,
// generates the SSH demo host key, renders the demo config, spawns the
// wanproxy daemon as a child process, and runs the SSH echo endpoint
// in-process.  It guarantees the child never outlives this process.
package daemon

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hangtiancheng/wanproxy/srv.go/internal/bench"
	"github.com/hangtiancheng/wanproxy/srv.go/internal/paths"
)

// Daemon owns the wanproxy child process and the in-process SSH echo.
type Daemon struct {
	cmd  *exec.Cmd
	echo *bench.SSHEchoServer

	mu        sync.Mutex
	stopped   bool
	restartMu sync.Mutex // serializes Restart against itself
}

// New builds a Daemon (call Start to bring the fleet up).
func New() *Daemon { return &Daemon{} }

// Start brings up the whole demo fleet.  On any failure it tears down
// whatever it already started before returning the error.
func (d *Daemon) Start() error {
	if err := d.prepare(); err != nil {
		d.Stop()
		return err
	}
	if err := d.spawnWanproxy(); err != nil {
		d.Stop()
		return err
	}
	monitorPort := paths.MonitorPort()
	if err := waitForHTTP(monitorPort, "wanproxy monitor", 15*time.Second); err != nil {
		d.Stop()
		return err
	}
	log.Printf("[daemon] wanproxy daemon up (monitor on :%d)", monitorPort)

	echoAddr := fmt.Sprintf("127.0.0.1:%d", paths.SSHEchoPort())
	echo, err := bench.ServeSSHEcho(echoAddr, paths.HostKeyPath)
	if err != nil {
		d.Stop()
		return fmt.Errorf("start ssh echo: %w", err)
	}
	d.echo = echo
	if err := waitForTCP(paths.SSHEchoPort(), "ssh echo", 10*time.Second); err != nil {
		d.Stop()
		return err
	}
	log.Printf("[daemon] ssh echo up on :%d", paths.SSHEchoPort())
	return nil
}

// prepare ensures dirs, binaries, host key and rendered config exist.
func (d *Daemon) prepare() error {
	paths.EnsureDirs()
	if err := ensureBinaries(); err != nil {
		return err
	}
	if err := ensureHostKey(); err != nil {
		return err
	}
	return renderConfig()
}

func ensureBinaries() error {
	if fileExists(paths.WanproxyBin) && fileExists(paths.TackBin) && fileExists(paths.XcdumpBin) {
		return nil
	}
	log.Printf("[daemon] building wanproxy.go binaries (make build)...")
	cmd := exec.Command("make", "build")
	cmd.Dir = paths.WANPROXY_GO_ROOT
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("make build: %w", err)
	}
	return nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// ensureHostKey reuses data/ssh-hostkey.pem when present, otherwise
// generates a demo-only RSA-2048 PKCS#1 PEM key (mode 0600).
func ensureHostKey() error {
	if fileExists(paths.HostKeyPath) {
		return nil
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generate host key: %w", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	data := pem.EncodeToMemory(block)
	if err := os.WriteFile(paths.HostKeyPath, data, 0o600); err != nil {
		return fmt.Errorf("write host key: %w", err)
	}
	log.Printf("[daemon] generated demo SSH host key at %s", paths.HostKeyPath)
	return nil
}

// renderConfig renders demo/wanproxy.conf.template into data/wanproxy.conf.
func renderConfig() error {
	tpl, err := os.ReadFile(paths.TemplatePath)
	if err != nil {
		return fmt.Errorf("read template: %w", err)
	}
	out := string(tpl)
	out = strings.ReplaceAll(out, "{{SRV_PORT}}", strconv.Itoa(paths.SRVPort()))
	out = strings.ReplaceAll(out, "{{MONITOR_PORT}}", strconv.Itoa(paths.MonitorPort()))
	out = strings.ReplaceAll(out, "{{SSH_ECHO_PORT}}", strconv.Itoa(paths.SSHEchoPort()))
	out = strings.ReplaceAll(out, "{{XCACHE_PATH}}", paths.XCachePath)
	out = strings.ReplaceAll(out, "{{HOSTKEY_PATH}}", paths.HostKeyPath)
	if err := os.WriteFile(paths.ConfigPath, []byte(out), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	log.Printf("[daemon] rendered daemon config at %s", paths.ConfigPath)
	return nil
}

// spawnWanproxy launches the wanproxy daemon and pipes its output into
// the Go log with a [wanproxy] prefix.
func (d *Daemon) spawnWanproxy() error {
	cmd := exec.Command(paths.WanproxyBin, "-c", paths.ConfigPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn wanproxy: %w", err)
	}
	d.cmd = cmd
	go pipeLines(stdout, "wanproxy")
	go pipeLines(stderr, "wanproxy")
	go func() {
		err := cmd.Wait()
		d.mu.Lock()
		stopped := d.stopped
		d.mu.Unlock()
		if !stopped {
			if err != nil {
				log.Printf("[wanproxy] exited: %v", err)
			} else {
				log.Printf("[wanproxy] exited")
			}
		}
	}()
	return nil
}

func pipeLines(r io.Reader, label string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		log.Printf("[%s] %s", label, line)
	}
}

// Restart stops the fleet and brings it up again, rendering a fresh
// config and waiting for the monitor and the SSH echo.  When clearCache
// is set the XCDFS volume is deleted first, so the fleet restarts with
// a cold dedup cache (the in-memory cache is always cold after a
// restart); this is how experiments produce a true first-sync baseline.
func (d *Daemon) Restart(clearCache bool) error {
	d.restartMu.Lock()
	defer d.restartMu.Unlock()

	d.Stop()

	d.mu.Lock()
	d.stopped = false
	d.cmd = nil
	d.echo = nil
	d.mu.Unlock()

	if clearCache {
		if err := os.Remove(paths.XCachePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clear XCDFS volume: %w", err)
		}
		log.Printf("[daemon] cleared XCDFS volume %s", paths.XCachePath)
	}

	log.Printf("[daemon] restarting wanproxy fleet (clearCache=%v)", clearCache)
	return d.Start()
}

// Stop tears down the SSH echo and the wanproxy child (SIGTERM, then
// SIGKILL after a grace period).  It is idempotent.
func (d *Daemon) Stop() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	echo := d.echo
	cmd := d.cmd
	d.mu.Unlock()

	if echo != nil {
		if err := echo.Close(); err != nil {
			log.Printf("[daemon] ssh echo close: %v", err)
		}
		log.Printf("[daemon] ssh echo stopped")
	}
	if cmd != nil && cmd.Process != nil {
		log.Printf("[daemon] stopping wanproxy daemon (pid %d)", cmd.Process.Pid)
		if runtime.GOOS == "windows" {
			// Windows has no SIGTERM; termination is always forceful.
			_ = cmd.Process.Kill()
		} else {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
		done := make(chan struct{})
		go func() {
			_, _ = cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
			log.Printf("[daemon] wanproxy daemon stopped")
		case <-time.After(3 * time.Second):
			log.Printf("[daemon] wanproxy did not exit in 3s; sending SIGKILL")
			_ = cmd.Process.Kill()
			<-done
			log.Printf("[daemon] wanproxy daemon killed")
		}
	}
}

func waitForHTTP(port int, label string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	url := fmt.Sprintf("http://127.0.0.1:%d/status", port)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			code := resp.StatusCode
			resp.Body.Close()
			if (code >= 200 && code < 300) || code == 404 {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s did not come up on :%d within %s", label, port, timeout)
}

func waitForTCP(port int, label string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s did not come up on :%d within %s", label, port, timeout)
}
