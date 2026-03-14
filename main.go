package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/mbentley/discord-queue/internal/alert"
	"github.com/mbentley/discord-queue/internal/config"
	"github.com/mbentley/discord-queue/internal/delivery"
	"github.com/mbentley/discord-queue/internal/metrics"
	"github.com/mbentley/discord-queue/internal/server"
	"github.com/mbentley/discord-queue/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("failed to open database", "path", cfg.DBPath, "err", err)
		os.Exit(1)
	}
	defer s.Close()

	reset, err := s.ResetInFlight()
	if err != nil {
		slog.Error("failed to reset in_flight messages on startup", "err", err)
		os.Exit(1)
	}
	if reset > 0 {
		slog.Warn("reset in_flight messages to pending after unclean shutdown", "count", reset)
	}

	m := metrics.New(func() float64 {
		n, _ := s.QueueDepth()
		return float64(n)
	})

	a := alert.New(cfg)
	e := delivery.New(s, cfg, m, a)
	srv := server.New(cfg, s, e)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	// Start delivery engine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.Run(ctx)
	}()

	// Start HTTP server.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server failed", "err", err)
			cancel() // trigger shutdown of the delivery engine too
		}
	}()

	// Wait for SIGTERM or SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig)

	// Stop accepting new ingest requests first.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP server shutdown error", "err", err)
	}

	// Signal the delivery engine to stop.
	cancel()

	wg.Wait()
	slog.Info("shutdown complete")
}
