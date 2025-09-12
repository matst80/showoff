package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/matst80/showoff/internal/obs"
)

// main is kept intentionally small; handlers live in listener_handlers.go
func main() {
	if cfg.Debug {
		obs.EnableDebug(true)
	}
	obs.Info("server.start", obs.Fields{"control": cfg.ControlAddr, "public": cfg.PublicAddr, "data": cfg.DataAddr, "metrics": cfg.MetricsAddr})
	state, err := newStateStore(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		obs.Error("state.init", obs.Fields{"err": err.Error()})
		os.Exit(1)
	}
	// Start redis maintenance loop if applicable
	if rs, ok := state.(*redisStateStore); ok {
		go rs.startMaintenance(context.Background())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctrlLn, err := net.Listen("tcp", cfg.ControlAddr)
	if err != nil {
		obs.Error("listen.control", obs.Fields{"err": err.Error(), "addr": cfg.ControlAddr})
		os.Exit(1)
	}
	defer ctrlLn.Close()
	dataLn, err := net.Listen("tcp", cfg.DataAddr)
	if err != nil {
		obs.Error("listen.data", obs.Fields{"err": err.Error(), "addr": cfg.DataAddr})
		os.Exit(1)
	}
	defer dataLn.Close()
	pubLn, err := net.Listen("tcp", cfg.PublicAddr)
	if err != nil {
		obs.Error("listen.public", obs.Fields{"err": err.Error(), "addr": cfg.PublicAddr})
		os.Exit(1)
	}
	defer pubLn.Close()
	if !cfg.DisableMetrics {
		go startMetricsServer(cfg.MetricsAddr, state)
	} else if cfg.Debug {
		obs.Info("metrics.disabled", obs.Fields{})
	}
	go runCleanupLoop(ctx, state, cfg.CleanupInterval, cfg.RequestTimeout)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); acceptControl(ctx, ctrlLn, state) }()
	wg.Add(1)
	go func() { defer wg.Done(); acceptData(ctx, dataLn, state) }()
	wg.Add(1)
	go func() { defer wg.Done(); acceptPublic(ctx, pubLn, state) }()
	state.setReady(true)
	obs.Info("server.ready", obs.Fields{})
	<-ctx.Done()
	obs.Info("server.shutdown.signal", obs.Fields{})
	state.setClosing(true)
	_ = ctrlLn.Close()
	_ = dataLn.Close()
	_ = pubLn.Close()
	state.cleanupExpiredPending(cfg.RequestTimeout)
	wg.Wait()
	obs.Info("server.shutdown.complete", obs.Fields{})
}
