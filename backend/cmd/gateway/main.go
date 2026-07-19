package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Amsors/Bosun/backend/internal/config"
	"github.com/Amsors/Bosun/backend/internal/database"
	db "github.com/Amsors/Bosun/backend/internal/database/sqlc"
	"github.com/Amsors/Bosun/backend/internal/gateway"
	"github.com/Amsors/Bosun/backend/internal/logging"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load(config.ComponentGateway)
	if err != nil {
		slog.Error("invalid configuration", "reason", err)
		return 1
	}
	logger := logging.New(cfg.LogLevel, string(config.ComponentGateway))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.Open(ctx, cfg.DatabaseURL, cfg.DatabaseConnectTimeout)
	if err != nil {
		logger.Error("database connection failed", "reason", err)
		return 1
	}
	defer pool.Close()

	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		logger.Error("kubernetes configuration failed", "reason", "in_cluster_config_unavailable")
		return 1
	}
	k8sConfig.Timeout = cfg.Gateway.TokenReviewTimeout
	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		logger.Error("kubernetes client creation failed", "reason", "kubernetes_client_unavailable")
		return 1
	}

	resolver := gateway.NewDatabaseSessionResolver(db.New(pool), cfg.Gateway.SessionLookupTimeout)
	authenticator := gateway.NewAuthenticator(
		clientset.AuthenticationV1().TokenReviews(),
		resolver,
		cfg.Gateway.TokenReviewTimeout,
	)
	metrics := gateway.NewMetrics()
	handler, err := gateway.NewHandler(gateway.HandlerConfig{
		UpstreamURL:        cfg.Gateway.UpstreamURL,
		UpstreamAPIKey:     cfg.Gateway.UpstreamAPIKey,
		Provider:           cfg.Gateway.Provider,
		UpstreamAuthHeader: cfg.Gateway.UpstreamAuthHeader,
		UpstreamAuthScheme: cfg.Gateway.UpstreamAuthScheme,
		UpstreamTimeout:    cfg.Gateway.UpstreamTimeout,
	}, authenticator, pool, metrics, logger)
	if err != nil {
		logger.Error("gateway handler creation failed", "reason", "invalid_gateway_configuration")
		return 1
	}

	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
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
