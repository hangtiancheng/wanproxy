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

	"github.com/hangtiancheng/wanproxy/wanproxy/common"
	"github.com/hangtiancheng/wanproxy/wanproxy/config"
	"github.com/hangtiancheng/wanproxy/wanproxy/monitor"
	"github.com/hangtiancheng/wanproxy/wanproxy/proxy"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "wanproxy: %v\n", err)
		os.Exit(1)
	}
}

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

	log.Info("WANProxy")

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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	log.Info("shutting down")
	if mon != nil {
		err = mon.Stop()
	}
	return err
}
