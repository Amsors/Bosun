package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Amsors/Bosun/backend/internal/app"
	"github.com/Amsors/Bosun/backend/internal/config"
	"github.com/Amsors/Bosun/backend/internal/database"
	"github.com/Amsors/Bosun/backend/internal/logging"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load(config.ComponentAPI)
	if err != nil {
		slog.Error("invalid configuration", "reason", err)
		return 1
	}
	logger := logging.New(cfg.LogLevel, string(config.ComponentAPI))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.Open(ctx, cfg.DatabaseURL, cfg.DatabaseConnectTimeout)
	if err != nil {
		logger.Error("database connection failed", "reason", err)
		return 1
	}
	defer pool.Close()

	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           app.NewRouter(string(config.ComponentAPI), pool),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
	}
	errs := make(chan error, 1)
	go func() {
		logger.Info("server started", "reason", "listening", "address", cfg.ListenAddress)
		errs <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case err = <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", "reason", err)
			return 1
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown failed", "reason", err)
		return 1
	}
	logger.Info("server stopped", "reason", "shutdown")
	return 0
}
