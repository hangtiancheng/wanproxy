// Command wanbench is a thin debugging CLI over the internal/bench
// library.  It mirrors the former srv/bench command:
//
//	wanbench run -scenario http -base http://127.0.0.1:8320 \
//	         -direction upload|download|roundtrip \
//	         -size bytes -seed n -pattern random|repeat|mixed
//	wanbench run -scenario socks -proxy 127.0.0.1:8330 -target 127.0.0.1:3101 ...
//	wanbench run -scenario ssh -proxy 127.0.0.1:8340 -size bytes -seed n -pattern p
//	wanbench ssh-echo -listen 127.0.0.1:8349 -hostkey key.pem
//	wanbench gen -size bytes -seed n -pattern p [-out file]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/hangtiancheng/wanproxy/srv.go/internal/bench"
	"github.com/hangtiancheng/wanproxy/srv.go/internal/payload"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "run":
		runCmd(os.Args[2:])
	case "ssh-echo":
		sshEchoCmd(os.Args[2:])
	case "gen":
		genCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: wanbench {run|ssh-echo|gen} [flags]")
	os.Exit(2)
}

// cliResult is the JSON output of one benchmark run.  It embeds the
// library Result so the field names match the former bench/main.go.
type cliResult struct {
	Scenario  string `json:"scenario"`
	Direction string `json:"direction"`
	Pattern   string `json:"pattern"`
	Size      int64  `json:"size"`
	Seed      int64  `json:"seed"`
	bench.Result
}

func emit(r cliResult) {
	out, _ := json.Marshal(r)
	fmt.Println(string(out))
}

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	scenario := fs.String("scenario", "", "http|socks|ssh")
	base := fs.String("base", "", "HTTP base URL (http scenario)")
	proxyAddr := fs.String("proxy", "", "proxy address (socks/ssh)")
	target := fs.String("target", "", "target host:port (socks)")
	direction := fs.String("direction", "roundtrip", "upload|download|roundtrip")
	size := fs.Int64("size", 16<<20, "payload size in bytes")
	seed := fs.Int64("seed", 42, "payload seed")
	patternStr := fs.String("pattern", "mixed", "random|repeat|mixed")
	_ = fs.Parse(args)

	header := cliResult{
		Scenario: *scenario, Direction: *direction, Pattern: *patternStr,
		Size: *size, Seed: *seed,
	}

	pattern, err := payload.ParsePattern(*patternStr)
	if err != nil {
		header.Result = bench.Result{OK: false, Error: err.Error()}
		emit(header)
		os.Exit(1)
	}

	res := bench.Run(*scenario, *base, *proxyAddr, *target, *direction, pattern, *size, *seed)
	header.Result = res
	emit(header)
	if !res.OK {
		os.Exit(1)
	}
}

func sshEchoCmd(args []string) {
	fs := flag.NewFlagSet("ssh-echo", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8349", "listen address")
	hostkey := fs.String("hostkey", "", "RSA private key PEM path")
	_ = fs.Parse(args)

	if *hostkey == "" {
		fmt.Fprintln(os.Stderr, "ssh-echo requires -hostkey")
		os.Exit(2)
	}
	srv, err := bench.ServeSSHEcho(*listen, *hostkey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "ssh echo listening on %s\n", srv.Addr())
	select {} // serve until killed
}

func genCmd(args []string) {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	size := fs.Int64("size", 1<<20, "payload size")
	seed := fs.Int64("seed", 42, "payload seed")
	patternStr := fs.String("pattern", "mixed", "random|repeat|mixed")
	out := fs.String("out", "", "output file (default stdout)")
	_ = fs.Parse(args)

	pattern, err := payload.ParsePattern(*patternStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var w io.Writer = os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}
	if _, err := io.Copy(w, payload.NewReader(pattern, *size, *seed)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
