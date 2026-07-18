package config

import (
	"fmt"
	"log/slog"
	"os"
	"time"
)

type Component string

const (
	ComponentAPI     Component = "api"
	ComponentGateway Component = "gateway"
)

type Config struct {
	Component              Component
	ListenAddress          string
	DatabaseURL            string
	LogLevel               slog.Level
	DatabaseConnectTimeout time.Duration
	ReadHeaderTimeout      time.Duration
	ShutdownTimeout        time.Duration
}

func Load(component Component) (Config, error) {
	if component != ComponentAPI && component != ComponentGateway {
		return Config{}, fmt.Errorf("unsupported component %q", component)
	}

	prefix := "BOSUN_API_"
	defaultAddress := ":8080"
	if component == ComponentGateway {
		prefix = "BOSUN_GATEWAY_"
		defaultAddress = ":8081"
	}

	cfg := Config{
		Component:              component,
		ListenAddress:          valueOrDefault(prefix+"LISTEN_ADDRESS", defaultAddress),
		DatabaseURL:            os.Getenv("BOSUN_DATABASE_URL"),
		DatabaseConnectTimeout: 5 * time.Second,
		ReadHeaderTimeout:      5 * time.Second,
		ShutdownTimeout:        10 * time.Second,
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("BOSUN_DATABASE_URL is required")
	}

	var err error
	if cfg.LogLevel, err = parseLogLevel(valueOrDefault("BOSUN_LOG_LEVEL", "info")); err != nil {
		return Config{}, err
	}
	if cfg.DatabaseConnectTimeout, err = duration(prefix+"DATABASE_CONNECT_TIMEOUT", cfg.DatabaseConnectTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ReadHeaderTimeout, err = duration(prefix+"READ_HEADER_TIMEOUT", cfg.ReadHeaderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = duration(prefix+"SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func valueOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func duration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", key, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return value, nil
}

func parseLogLevel(raw string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("BOSUN_LOG_LEVEL is invalid: %w", err)
	}
	return level, nil
}
