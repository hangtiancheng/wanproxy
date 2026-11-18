package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultServerPort  = 3101
	defaultClientPort  = 5173
	defaultMonitorPort = 9901
	defaultSSHPort     = 8349
)

type options struct {
	serverPort  int
	clientPort  int
	monitorPort int
	sshEchoPort int
}

func main() {
	args := os.Args[1:]
	command := "help"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	opts, err := parseOptions(args)
	if err != nil {
		fail(err)
	}
	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	if err := dispatch(command, root, opts); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "[stack] %v\n", err)
	os.Exit(1)
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stdout, "[stack] "+format+"\n", args...)
}

func parseOptions(args []string) (options, error) {
	opts := options{
		serverPort:  envInt("SERVER_PORT", defaultServerPort),
		clientPort:  envInt("CLIENT_PORT", defaultClientPort),
		monitorPort: envInt("MONITOR_PORT", defaultMonitorPort),
		sshEchoPort: envInt("SSH_ECHO_PORT", defaultSSHPort),
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			return opts, fmt.Errorf("unknown argument %q", arg)
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !hasValue {
			if i+1 >= len(args) {
				return opts, fmt.Errorf("missing value for --%s", name)
			}
			i++
			value = args[i]
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return opts, fmt.Errorf("invalid value for --%s: %q", name, value)
		}
		switch name {
		case "server-port":
			opts.serverPort = n
		case "client-port":
			opts.clientPort = n
		case "monitor-port":
			opts.monitorPort = n
		case "ssh-echo-port":
			opts.sshEchoPort = n
		default:
			return opts, fmt.Errorf("unknown option --%s", name)
		}
	}
	return opts, nil
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func repoRoot() (string, error) {
	var candidates []string
	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file)))))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(candidate, "Makefile")); err == nil {
			return candidate, nil
		}
	}
	if len(candidates) > 0 {
		return candidates[0], nil
	}
	return "", fmt.Errorf("cannot locate repository root")
}

func dispatch(command, root string, opts options) error {
	switch command {
	case "help", "--help", "-h":
		help(opts)
		return nil
	case "dev":
		return dev(root, opts)
	case "server":
		return server(root, opts)
	case "client":
		return client(root, opts)
	case "start":
		return start(root, opts)
	case "stop":
		return stop(root)
	case "restart":
		if err := stop(root); err != nil {
			return err
		}
		return start(root, opts)
	case "status":
		return status(root)
	case "logs":
		return logs(root)
	case "open":
		return openBrowser(opts)
	case "install":
		return install(root, opts)
	case "build":
		if err := buildGo(root); err != nil {
			return err
		}
		return buildClient(root)
	case "build-go":
		return buildGo(root)
	case "build-client":
		return buildClient(root)
	case "test":
		if err := testGo(root); err != nil {
			return err
		}
		return testClient(root)
	case "test-go":
		return testGo(root)
	case "test-client":
		return testClient(root)
	case "clean":
		return clean(root)
	default:
		help(opts)
		return fmt.Errorf("unknown command %q", command)
	}
}

func help(opts options) {
	fmt.Print(`WANProxy demo stack (Go backend + client dashboard)

  dev       backend + client in the foreground (Ctrl-C stops both)
  server    backend only, foreground
  client    client front-end only, foreground
  start     backend + client backgrounded (logs and pidfiles in server/data/)
  stop      stop the background processes
  restart   stop + start
  status    show background process state
  logs      follow the background logs
  open      open the client front-end in a browser
  install   install client dependencies and build the Go backend
  build     build the Go backend and client
  test      run Go tests/vet and client build/lint
  clean     remove build artifacts (runtime data is kept)

`)
	fmt.Printf("  ports: server=%d client=%d monitor=%d sshEcho=%d\n",
		opts.serverPort, opts.clientPort, opts.monitorPort, opts.sshEchoPort)
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func serverBinary(root string) string {
	return filepath.Join(root, "server", "bin", "wanproxy-server"+exeSuffix())
}

func backendEnv(opts options) []string {
	return []string{
		"MONITOR_PORT=" + strconv.Itoa(opts.monitorPort),
		"PORT=" + strconv.Itoa(opts.serverPort),
		"SERVER_PORT=" + strconv.Itoa(opts.serverPort),
		"SSH_ECHO_PORT=" + strconv.Itoa(opts.sshEchoPort),
	}
}

func command(name string, args ...string) *exec.Cmd {
	if runtime.GOOS == "windows" && (name == "pnpm" || name == "npm" || name == "npx") {
		return exec.Command("cmd", append([]string{"/c", name}, args...)...)
	}
	return exec.Command(name, args...)
}

func run(dir string, env []string, name string, args ...string) error {
	cmd := command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func buildGo(root string) error {
	wanproxyDir := filepath.Join(root, "wanproxy")
	if err := run(wanproxyDir, nil, "go", "build", "./..."); err != nil {
		return err
	}
	binDir := filepath.Join(wanproxyDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"wanproxy", "xcdump", "tack", "fwdproxy", "diskdup"} {
		target := filepath.Join("bin", name+exeSuffix())
		if err := run(wanproxyDir, nil, "go", "build", "-o", target, "./cmd/"+name); err != nil {
			return err
		}
	}

	serverDir := filepath.Join(root, "server")
	if err := os.MkdirAll(filepath.Join(serverDir, "bin"), 0o755); err != nil {
		return err
	}
	return run(serverDir, nil, "go", "build", "-o", filepath.Join("bin", "wanproxy-server"+exeSuffix()), "./cmd/server")
}

func buildClient(root string) error {
	return run(root, nil, "pnpm", "--dir", "client", "build")
}

func install(root string, opts options) error {
	if err := run(root, nil, "pnpm", "--dir", "client", "install"); err != nil {
		return err
	}
	return buildGo(root)
}

func server(root string, opts options) error {
	if err := buildGo(root); err != nil {
		return err
	}
	return run(root, backendEnv(opts), serverBinary(root))
}

func client(root string, opts options) error {
	return run(root, nil, "pnpm", "--dir", "client", "dev", "--port", strconv.Itoa(opts.clientPort))
}

func dev(root string, opts options) error {
	if err := buildGo(root); err != nil {
		return err
	}
	logf("backend -> http://127.0.0.1:%d", opts.serverPort)
	logf("client -> http://localhost:%d", opts.clientPort)

	backend := command(serverBinary(root))
	backend.Dir = root
	backend.Env = append(os.Environ(), backendEnv(opts)...)
	backend.Stdin = os.Stdin
	backend.Stdout = os.Stdout
	backend.Stderr = os.Stderr

	clientProc := command("pnpm", "--dir", "client", "dev", "--port", strconv.Itoa(opts.clientPort))
	clientProc.Dir = root
	clientProc.Stdin = os.Stdin
	clientProc.Stdout = os.Stdout
	clientProc.Stderr = os.Stderr

	if err := backend.Start(); err != nil {
		return fmt.Errorf("start backend: %w", err)
	}
	if err := clientProc.Start(); err != nil {
		killTree(backend.Process.Pid)
		return fmt.Errorf("start client: %w", err)
	}

	done := make(chan error, 2)
	go func() { done <- backend.Wait() }()
	go func() { done <- clientProc.Wait() }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	select {
	case <-sig:
		killTree(backend.Process.Pid)
		killTree(clientProc.Process.Pid)
		<-done
		<-done
		return nil
	case err := <-done:
		killTree(backend.Process.Pid)
		killTree(clientProc.Process.Pid)
		<-done
		return err
	}
}

func start(root string, opts options) error {
	if err := buildGo(root); err != nil {
		return err
	}
	pidDir := filepath.Join(root, "server", "data", "pids")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		return err
	}

	if err := startOne(pidDir, "server", filepath.Join(root, "server", "data", "server.log"),
		backendEnv(opts), serverBinary(root), root, nil); err != nil {
		return err
	}
	clientCmd := command("pnpm", "--dir", "client", "dev", "--port", strconv.Itoa(opts.clientPort))
	if err := startOne(pidDir, "client", filepath.Join(root, "server", "data", "client.log"),
		nil, "", root, clientCmd); err != nil {
		return err
	}
	logf("backend -> http://127.0.0.1:%d  log: server/data/server.log", opts.serverPort)
	logf("client -> http://localhost:%d  log: server/data/client.log", opts.clientPort)
	return nil
}

func startOne(pidDir, name, logPath string, env []string, binary, root string, extra *exec.Cmd) error {
	pidFile := filepath.Join(pidDir, name+".pid")
	if pid, ok := readPid(pidFile); ok && processAlive(pid) {
		logf("%s already running (pid %d)", name, pid)
		return nil
	}

	var cmd *exec.Cmd
	if extra != nil {
		cmd = extra
	} else {
		cmd = command(binary)
	}
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		killTree(cmd.Process.Pid)
		return err
	}
	logf("%s started (pid %d)", name, cmd.Process.Pid)
	return nil
}

func stop(root string) error {
	pidDir := filepath.Join(root, "server", "data", "pids")
	for _, name := range []string{"server", "client"} {
		pidFile := filepath.Join(pidDir, name+".pid")
		pid, ok := readPid(pidFile)
		if ok {
			killTree(pid)
			logf("stopped %s (pid %d)", name, pid)
		}
		if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func status(root string) error {
	pidDir := filepath.Join(root, "server", "data", "pids")
	for _, name := range []string{"server", "client"} {
		pid, ok := readPid(filepath.Join(pidDir, name+".pid"))
		switch {
		case !ok:
			logf("%s: not running", name)
		case processAlive(pid):
			logf("%s: running (pid %d)", name, pid)
		default:
			logf("%s: stale pidfile (pid %d)", name, pid)
		}
	}
	return nil
}

func logs(root string) error {
	paths := []string{
		filepath.Join(root, "server", "data", "server.log"),
		filepath.Join(root, "server", "data", "client.log"),
	}
	offsets := make(map[string]int64)
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		start := info.Size() - 4096
		if start < 0 {
			start = 0
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		if _, err := file.Seek(start, io.SeekStart); err != nil {
			file.Close()
			return err
		}
		fmt.Printf("==> %s <==\n", path)
		if _, err := io.Copy(os.Stdout, file); err != nil {
			file.Close()
			return err
		}
		file.Close()
		offsets[path] = info.Size()
	}
	if len(offsets) == 0 {
		logf("no logs yet")
		return nil
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	for {
		select {
		case <-sig:
			return nil
		case <-ticker.C:
			for path, offset := range offsets {
				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				if info.Size() < offset {
					offsets[path] = 0
					offset = 0
				}
				if info.Size() == offset {
					continue
				}
				file, err := os.Open(path)
				if err != nil {
					continue
				}
				if _, err := file.Seek(offset, io.SeekStart); err != nil {
					file.Close()
					continue
				}
				n, err := io.Copy(os.Stdout, file)
				file.Close()
				if err != nil {
					return err
				}
				offsets[path] = offset + n
			}
		}
	}
}

func openBrowser(opts options) error {
	url := fmt.Sprintf("http://localhost:%d", opts.clientPort)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("open %s: %w", url, err)
	}
	return nil
}

func testGo(root string) error {
	if err := run(filepath.Join(root, "wanproxy"), nil, "go", "test", "./..."); err != nil {
		return err
	}
	serverDir := filepath.Join(root, "server")
	if err := run(serverDir, nil, "go", "vet", "./..."); err != nil {
		return err
	}
	return run(serverDir, nil, "go", "test", "./...")
}

func testClient(root string) error {
	if err := buildClient(root); err != nil {
		return err
	}
	return run(root, nil, "pnpm", "--dir", "client", "lint")
}

func clean(root string) error {
	for _, path := range []string{
		filepath.Join(root, "wanproxy", "bin"),
		filepath.Join(root, "server", "bin"),
		filepath.Join(root, "client", "dist"),
	} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	logf("cleaned build artifacts (runtime data kept)")
	return nil
}

func readPid(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
