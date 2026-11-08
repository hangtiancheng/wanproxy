package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hangtiancheng/wanproxy/srv/internal/benchsvc"
	"github.com/hangtiancheng/wanproxy/srv/internal/daemon"
	"github.com/hangtiancheng/wanproxy/srv/internal/files"
	"github.com/hangtiancheng/wanproxy/srv/internal/monitor"
	"github.com/hangtiancheng/wanproxy/srv/internal/paths"
	"github.com/hangtiancheng/wanproxy/srv/internal/server"
	"github.com/hangtiancheng/wanproxy/srv/internal/tools"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("[daemon] starting wanproxy-server (srv root %s)", paths.SRV_ROOT)

	d := daemon.New()
	if err := d.Start(); err != nil {
		log.Printf("[daemon] startup failed: %v", err)
		d.Stop()
		os.Exit(1)
	}

	filesSvc := files.NewService()
	monSvc := monitor.NewService()
	benchSvc := benchsvc.NewService(monSvc)
	toolsSvc := tools.NewService(filesSvc)
	app := server.New(filesSvc, monSvc, benchSvc, toolsSvc, d)

	addr := fmt.Sprintf("127.0.0.1:%d", paths.SRVPort())
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           app,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("[http] listening on %s", addr)
		errCh <- httpServer.ListenAndServe()
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case s := <-sig:
		log.Printf("[http] received %s; shutting down", s)
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Printf("[http] server failed: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("[http] shutdown: %v", err)
	}
	d.Stop()
	log.Printf("[daemon] shutdown complete")
}
