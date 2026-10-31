// Command wanproxy is the Go port of the C++ WANProxy daemon.
//
// Usage: wanproxy [-q | -v] -c configfile
//
// The config file may use the classic wanproxy.conf directive format of
// the C++ original or the JSON format of the TypeScript port.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/common"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/config"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/monitor"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/proxy"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "wanproxy: %v\n", err)
		os.Exit(1)
	}
}

// fleetSource adapts the loaded config and running fleet to the monitor
// Source interface.
type fleetSource struct {
	objects []config.Object
	fleet   *proxy.Fleet
}

func (f *fleetSource) Objects() []config.Object  { return f.objects }
func (f *fleetSource) Status() proxy.FleetStatus { return f.fleet.Status() }

func run() error {
	var (
		configFile = flag.String("c", "", "configuration file (wanproxy.conf or JSON)")
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

	log := common.Component("/wanproxy")

	// The C++ main() prints the banner before installing any masks,
	// so it appears even under -q.
	log.Info("WANProxy (Go port)")
	log.Info("Copyright (c) 2008-2016 WANProxy.org. All rights reserved.")

	// Install log masks in the C++ order.  With -q or -v the
	// command-line catch-all goes in first, exactly like the C++
	// main()'s Log::mask(".?", level) before config.configure(): since
	// the first matching mask wins, log-mask objects in the file cannot
	// override the command line.  Without them the configuration's
	// per-component masks apply as written, and a catch-all at the
	// configured default level is appended afterwards for components
	// they do not match.
	switch {
	case *verbose:
		common.SetLevel(slog.LevelDebug)
	case *quiet:
		common.SetLevel(slog.LevelError)
	default:
		common.ResetMasks()
	}

	cfg, err := config.Load(*configFile)
	if err != nil {
		return fmt.Errorf("could not configure proxies: %w", err)
	}

	if !*verbose && !*quiet {
		if err := common.Mask(".?", cfg.LogLevel); err != nil {
			return err
		}
	}
	fleet := proxy.NewFleet(log)
	for _, p := range cfg.Proxies {
		pc := p
		listener := &proxy.Listener{
			Name:       pc.Name,
			Iface:      pc.Interface,
			Peer:       pc.Peer,
			IfaceCodec: pc.InterfaceCodec,
			PeerCodec:  pc.PeerCodec,
			Log:        common.Component("/wanproxy/proxy/" + pc.Name),
		}
		if pc.SSH != nil {
			listener.SSH = &proxy.SSHSettings{HostKey: pc.SSH.HostKey}
		}
		fleet.AddTCP(listener)
	}
	for _, s := range cfg.SocksProxies {
		sc := s
		fleet.AddSocks(&proxy.SocksListener{
			Name:  sc.Name,
			Iface: sc.Interface,
			Log:   common.Component("/wanproxy/proxy/" + sc.Name + "/socks"),
		})
	}

	if err := fleet.Start(); err != nil {
		return err
	}

	var mon *monitor.Server
	if cfg.Monitor != nil {
		mon = monitor.NewServer(cfg.Monitor.Interface, &fleetSource{objects: cfg.Objects, fleet: fleet})
		if err := mon.Start(); err != nil {
			fleet.Stop()
			return err
		}
	}

	for _, addr := range fleet.Addresses() {
		fmt.Printf("proxy listening on %s:%d\n", addr.Address, addr.Port)
	}
	if mon != nil {
		if addr := mon.Address(); addr != nil {
			fmt.Printf("monitor listening on %s:%d\n", addr.Address, addr.Port)
		}
	}

	// Wait for a signal, then shut everything down.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	log.Info("shutting down")
	if mon != nil {
		mon.Stop()
	}
	return fleet.Stop()
}
