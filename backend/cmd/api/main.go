package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	bosunv1alpha1 "github.com/Amsors/Bosun/operator/api/v1alpha1"

	"github.com/Amsors/Bosun/backend/internal/app"
	"github.com/Amsors/Bosun/backend/internal/auth"
	"github.com/Amsors/Bosun/backend/internal/config"
	"github.com/Amsors/Bosun/backend/internal/database"
	"github.com/Amsors/Bosun/backend/internal/logging"
	"github.com/Amsors/Bosun/backend/internal/ratelimit"
	"github.com/Amsors/Bosun/backend/internal/session"
	"github.com/Amsors/Bosun/backend/internal/terminal"
	"github.com/Amsors/Bosun/backend/internal/userenv"
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
	authCfg, err := config.LoadAuth()
	if err != nil {
		slog.Error("invalid auth configuration", "reason", err)
		return 1
	}
	terminalCfg, err := config.LoadTerminal()
	if err != nil {
		slog.Error("invalid terminal configuration", "reason", err)
		return 1
	}
	logger := logging.New(cfg.LogLevel, string(config.ComponentAPI))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := database.Migrate(ctx, cfg.DatabaseURL, cfg.DatabaseMigrateTimeout); err != nil {
		logger.Error("database migration failed", "reason", err)
		return 1
	}

	pool, err := database.Open(ctx, cfg.DatabaseURL, cfg.DatabaseConnectTimeout)
	if err != nil {
		logger.Error("database connection failed", "reason", err)
		return 1
	}
	defer pool.Close()

	k8sClient, err := newK8sClient()
	if err != nil {
		logger.Error("kubernetes client init failed", "reason", err)
		return 1
	}
	terminalRuntime, err := newTerminalRuntime()
	if err != nil {
		logger.Error("terminal Kubernetes clients init failed", "reason", err)
		return 1
	}

	issuer, err := auth.NewJWTIssuer(authCfg.Issuer, authCfg.JWTPrivateKey, authCfg.AccessTokenTTL)
	if err != nil {
		logger.Error("jwt issuer init failed", "reason", err)
		return 1
	}

	authStore := auth.NewPgxStore(pool)
	provisioner := userenv.NewCRProvisioner(k8sClient)
	service, err := auth.NewService(auth.Config{
		Store:           authStore,
		Issuer:          issuer,
		Env:             provisioner,
		Argon2:          authCfg.Argon2,
		RefreshTokenTTL: authCfg.RefreshTokenTTL,
		Logger:          logger,
	})
	if err != nil {
		logger.Error("auth service init failed", "reason", err)
		return 1
	}

	userEnvRepairer := userenv.NewRepairer(authStore, provisioner, authCfg.RepairInterval, logger)
	go userEnvRepairer.Run(ctx)

	sessionStore := session.NewPgxStore(pool)
	sessionRuntime := session.NewCRControl(k8sClient)
	sessionService, err := session.NewService(session.ServiceConfig{
		Store: sessionStore, Idempotency: authStore, Environment: provisioner,
		Runtime: sessionRuntime, Logger: logger,
	})
	if err != nil {
		logger.Error("session service init failed", "reason", err)
		return 1
	}
	sessionRepairer := session.NewRepairer(sessionStore, sessionRuntime, authCfg.RepairInterval, logger)
	go sessionRepairer.Run(ctx)
	projector := session.NewProjector(sessionStore, k8sClient, logger)
	go projector.Run(ctx)
	terminalHandler, err := terminal.NewHandler(terminal.Config{
		WriteQueueCapacity:  terminalCfg.WriteQueueCapacity,
		InputQueueCapacity:  terminalCfg.InputQueueCapacity,
		MaxFrameBytes:       terminalCfg.MaxFrameBytes,
		WriteTimeout:        terminalCfg.WriteTimeout,
		PongTimeout:         terminalCfg.PongTimeout,
		PingInterval:        terminalCfg.PingInterval,
		ActivityMinInterval: terminalCfg.ActivityMinInterval,
	}, issuer, sessionStore, terminalRuntime, logger)
	if err != nil {
		logger.Error("terminal handler init failed", "reason", err)
		return 1
	}

	handler := app.NewAPIRouter(app.APIDeps{
		Database:     pool,
		Auth:         service,
		JWT:          issuer,
		Store:        authStore,
		LoginByIP:    ratelimit.New(authCfg.LoginIPLimit, authCfg.LoginIPWindow, authCfg.LoginLimiterCap),
		LoginByEmail: ratelimit.New(authCfg.LoginEmailLimit, authCfg.LoginEmailWindow, authCfg.LoginLimiterCap),
		Cookie: app.CookieConfig{
			Name:   authCfg.RefreshCookieName,
			Path:   authCfg.RefreshCookiePath,
			Secure: authCfg.RefreshCookieSecure,
			TTL:    authCfg.RefreshTokenTTL,
		},
		TrustedProxyHeader: authCfg.TrustedProxyHeader,
		Sessions:           sessionService,
		Terminal:           terminalHandler,
	})

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

// newTerminalRuntime creates clients solely for terminal authorization and
// pods/exec. These clients are intentionally separate from ordinary API clients.
func newTerminalRuntime() (*terminal.KubernetesRuntime, error) {
	restCfg, err := ctrlconfig.GetConfig()
	if err != nil {
		return nil, err
	}
	return terminal.NewKubernetesRuntime(restCfg)
}

// newK8sClient 构造用于创建/读取 CR 的 typed client；使用 in-cluster 或本地 kubeconfig。
func newK8sClient() (client.WithWatch, error) {
	restCfg, err := ctrlconfig.GetConfig()
	if err != nil {
		return nil, err
	}
	scheme := runtime.NewScheme()
	if err := bosunv1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return client.NewWithWatch(restCfg, client.Options{Scheme: scheme})
}
