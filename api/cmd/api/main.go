// Package main is the entrypoint for the private-dns management API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/privatedns/api/internal/bootstrap"
	"github.com/privatedns/api/internal/config"
	"github.com/privatedns/api/internal/db"
	"github.com/privatedns/api/internal/logger"
	"github.com/privatedns/api/internal/pdns"
	"github.com/privatedns/api/internal/server"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(2)
	}
	log := logger.New(cfg.LogLevel)
	slog.SetDefault(log)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	pool, err := db.Connect(ctx, cfg.DatabaseDSN())
	if err != nil {
		log.Error("db.connect", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	pdnsClient := pdns.NewClient(cfg.PDNSURL, cfg.PDNSKey)

	if err := bootstrap.Run(ctx, pool, pdnsClient, cfg); err != nil {
		log.Error("bootstrap", "err", err)
		os.Exit(1)
	}

	srv := server.New(cfg, pool, pdnsClient, log)

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Start HTTP server.
	errCh := make(chan error, 1)
	go func() {
		log.Info("api.listen", "addr", cfg.Listen)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		log.Info("api.shutdown", "reason", "signal")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("api.serve", "err", err)
			os.Exit(1)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}
