package daemon

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hangtiancheng/wanproxy/srv/internal/bench"
	"github.com/hangtiancheng/wanproxy/srv/internal/paths"
)

type Daemon struct {
	cmd  *exec.Cmd
	echo *bench.SSHEchoServer

	mu        sync.Mutex
	stopped   bool
	restartMu sync.Mutex
}

func New() *Daemon { return &Daemon{} }

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
	log.Printf("[daemon] building wanproxy binaries (make build)...")
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

func renderConfig() error {
	data, err := os.ReadFile(paths.ExampleConfigPath)
	if err != nil {
		return fmt.Errorf("read example configuration: %w", err)
	}
	var file map[string]any
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse example configuration: %w", err)
	}
	ports := map[float64]int{3101: paths.SRVPort(), 9901: paths.MonitorPort(), 8349: paths.SSHEchoPort()}
	var apply func(any)
	apply = func(node any) {
		switch node := node.(type) {
		case map[string]any:
			for key, value := range node {
				switch key {
				case "port":
					if n, ok := value.(float64); ok {
						if port, ok := ports[n]; ok {
							node[key] = port
						}
					}
				case "path":
					if value == "wanproxy.xcache" {
						node[key] = paths.XCachePath
					}
				case "hostKey":
					node[key] = paths.HostKeyPath
				}
				apply(value)
			}
		case []any:
			for _, value := range node {
				apply(value)
			}
		}
	}
	apply(file)
	data, err = json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encode daemon configuration: %w", err)
	}
	if err := os.WriteFile(paths.ConfigPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	log.Printf("[daemon] rendered daemon config at %s", paths.ConfigPath)
	return nil
}

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
