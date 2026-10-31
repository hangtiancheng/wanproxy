// Command fwdproxy is the Go port of the C++ fwdproxy program: a
// simple file-driven port-forwarding proxy.
//
// Usage: fwdproxy [-q | -v] -c configfile
//
// Each configuration line holds four words:
//
//	interface-host interface-port remote-host remote-port
//
// and creates one plain TCP proxy forwarding the interface endpoint to
// the remote endpoint, without any codec.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/common"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/proxy"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fwdproxy: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configFile = flag.String("c", "", "configuration file")
		quiet      = flag.Bool("q", false, "log errors only")
		verbose    = flag.Bool("v", false, "log debug messages")
	)
	flag.Parse()

	if *configFile == "" {
		flag.Usage()
		return fmt.Errorf("no configuration file given")
	}
	if *quiet && *verbose {
		flag.Usage()
		return fmt.Errorf("-q and -v are mutually exclusive")
	}

	switch {
	case *verbose:
		common.SetLevel(slog.LevelDebug)
	case *quiet:
		common.SetLevel(slog.LevelError)
	default:
		common.SetLevel(slog.LevelInfo)
	}
	log := common.Component("/fwdproxy")

	data, err := os.ReadFile(*configFile)
	if err != nil {
		return fmt.Errorf("could not open file: %w", err)
	}

	fleet := proxy.NewFleet(log)
	for i, line := range strings.Split(string(data), "\n") {
		tokens := strings.Fields(line)
		if len(tokens) == 0 || strings.HasPrefix(tokens[0], "#") {
			continue
		}
		if len(tokens) != 4 {
			return fmt.Errorf("wrong number of items in configuration entry on line %d: expected 4, got %d", i+1, len(tokens))
		}
		fleet.AddTCP(&proxy.Listener{
			Name: fmt.Sprintf("configured-%d", i+1),
			Iface: proxy.Endpoint{
				Family: proxy.FamilyIP,
				Host:   tokens[0],
				Port:   tokens[1],
			},
			Peer: proxy.Endpoint{
				Family: proxy.FamilyIP,
				Host:   tokens[2],
				Port:   tokens[3],
			},
			Log: common.Component(fmt.Sprintf("/fwdproxy/proxy/%d", i+1)),
		})
	}

	if err := fleet.Start(); err != nil {
		return err
	}
	for _, addr := range fleet.Addresses() {
		fmt.Printf("proxy listening on %s:%d\n", addr.Address, addr.Port)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	log.Info("shutting down")
	return fleet.Stop()
}
