// Command wanproxy is the Go port of the C++ WANProxy daemon.
//
// Usage: wanproxy [-q | -v] -c configfile
//
// The config file uses JSON; see config/schema.json.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
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

func run() (err error) {
	var (
		configFile = flag.String("c", "", "JSON configuration file")
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
	defer func() { err = errors.Join(err, cfg.Close()) }()

	if !*verbose && !*quiet {
		for _, mask := range cfg.LogMasks {
			if err := common.Mask(mask.Regex, mask.Level); err != nil {
				return err
			}
		}
		if err := common.Mask(".?", cfg.LogLevel); err != nil {
			return err
		}
	}
	fleet := cfg.NewFleet(log)

	if err := fleet.Start(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, fleet.Stop()) }()

	var mon *monitor.Server
	if cfg.Monitor != nil {
		mon = monitor.NewServer(*cfg.Monitor, &fleetSource{objects: cfg.Objects, fleet: fleet})
		if err := mon.Start(); err != nil {
			return err
		}
	}

	for _, addr := range fleet.Addresses() {
		fmt.Printf("proxy listening on %s\n", net.JoinHostPort(addr.Address, strconv.Itoa(addr.Port)))
	}
	if mon != nil {
		if addr := mon.Address(); addr != nil {
			fmt.Printf("monitor listening on %s\n", net.JoinHostPort(addr.Address, strconv.Itoa(addr.Port)))
		}
	}

	// Wait for a signal, then shut everything down.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	log.Info("shutting down")
	if mon != nil {
		err = mon.Stop()
	}
	return err
}
