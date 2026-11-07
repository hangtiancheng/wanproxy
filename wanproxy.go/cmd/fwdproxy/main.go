package main

import (
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
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fwdproxy: %v\n", err)
		os.Exit(1)
	}
}

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

	switch {
	case *verbose:
		common.SetLevel(slog.LevelDebug)
	case *quiet:
		common.SetLevel(slog.LevelError)
	default:
		common.ResetMasks()
	}
	log := common.Component("/fwdproxy")

	cfg, err := config.Load(*configFile)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, cfg.Close()) }()
	if cfg.Monitor != nil {
		return fmt.Errorf("fwdproxy does not support a monitor")
	}
	for _, p := range cfg.Proxies {
		if (p.Type != "tcp" && p.Type != "udp") || p.InterfaceCodec != nil || p.PeerCodec != nil {
			return fmt.Errorf("fwdproxy only supports plain TCP/UDP proxies: %q", p.Name)
		}
	}
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
	for _, addr := range fleet.Addresses() {
		fmt.Printf("proxy listening on %s\n", net.JoinHostPort(addr.Address, strconv.Itoa(addr.Port)))
	}

	defer func() { err = errors.Join(err, fleet.Stop()) }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	<-sig

	log.Info("shutting down")
	return nil
}
