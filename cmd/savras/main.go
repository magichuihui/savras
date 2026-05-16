package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"savras/internal/auth"
	"savras/internal/config"
	"savras/internal/grafana"
	"savras/internal/proxy"
	"savras/internal/sync"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{AddSource: true})))

	cfg, err := config.LoadConfig()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	auth.Init(cfg)

	grafClient := grafana.NewClient(cfg.Server.GrafanaAddr, &cfg.Grafana)

	var worker *sync.SyncWorker
	if cfg.Sync.Enabled {
		worker = sync.StartSyncWorker(cfg, grafClient)
		proxy.SetSyncReadyFn(func() bool {
			select {
			case <-worker.Ready():
				return true
			default:
				return false
			}
		})
	}

	// Sync queue coalesces external triggers (manual, post-login) during
	// Grafana downtime into a single execution on recovery.
	var syncQueue *proxy.SyncQueue
	// Recovery callback: blocking sync that runs in the monitor's probe
	// loop to ensure traffic is blocked until permissions are rebuilt.
	var recoveryFn func()
	if worker != nil {
		doSync := func() {
			if err := worker.SyncNow(); err != nil {
				slog.Error("sync failed", "error", err)
			}
		}

		syncQueue = proxy.NewSyncQueue(doSync)
		proxy.SetSyncTriggerFn(func(ctx context.Context) error {
			syncQueue.Trigger()
			return nil
		})

		recoveryFn = doSync
	}
	monitor := proxy.NewGrafanaMonitor(cfg.Server.GrafanaAddr, recoveryFn)
	proxy.SetGrafanaMonitor(monitor)

	handler := proxy.NewProxyHandler(cfg)

	srv := &http.Server{
		Addr:    cfg.Server.ListenAddr,
		Handler: handler,
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("starting savras server", "addr", cfg.Server.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-sig
	slog.Info("shutting down")
	if worker != nil {
		worker.Stop()
	}
}
