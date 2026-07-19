package config

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

type Component string

const (
	ComponentAPI     Component = "api"
	ComponentGateway Component = "gateway"
)

var (
	providerNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	authSchemePattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._~-]*$`)
)

type Config struct {
	Component              Component
	ListenAddress          string
	DatabaseURL            string
	LogLevel               slog.Level
	DatabaseConnectTimeout time.Duration
	DatabaseMigrateTimeout time.Duration
	ReadHeaderTimeout      time.Duration
	ShutdownTimeout        time.Duration
	Gateway                GatewayConfig
}

type GatewayConfig struct {
	UpstreamURL          string
	UpstreamAPIKey       string
	Provider             string
	UpstreamAuthHeader   string
	UpstreamAuthScheme   string
	TokenReviewTimeout   time.Duration
	SessionLookupTimeout time.Duration
	UpstreamTimeout      time.Duration
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
		DatabaseMigrateTimeout: 30 * time.Second,
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
	if component == ComponentAPI {
		if cfg.DatabaseMigrateTimeout, err = duration(prefix+"DATABASE_MIGRATE_TIMEOUT", cfg.DatabaseMigrateTimeout); err != nil {
			return Config{}, err
		}
	}
	if cfg.ReadHeaderTimeout, err = duration(prefix+"READ_HEADER_TIMEOUT", cfg.ReadHeaderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = duration(prefix+"SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if component == ComponentGateway {
		if cfg.Gateway, err = loadGatewayConfig(); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func loadGatewayConfig() (GatewayConfig, error) {
	cfg := GatewayConfig{
		UpstreamURL:          os.Getenv("BOSUN_GATEWAY_UPSTREAM_URL"),
		UpstreamAPIKey:       os.Getenv("BOSUN_GATEWAY_UPSTREAM_API_KEY"),
		Provider:             valueOrDefault("BOSUN_GATEWAY_PROVIDER", "platform-default"),
		UpstreamAuthHeader:   http.CanonicalHeaderKey(valueOrDefault("BOSUN_GATEWAY_UPSTREAM_AUTH_HEADER", "x-api-key")),
		UpstreamAuthScheme:   os.Getenv("BOSUN_GATEWAY_UPSTREAM_AUTH_SCHEME"),
		TokenReviewTimeout:   3 * time.Second,
		SessionLookupTimeout: 3 * time.Second,
		UpstreamTimeout:      10 * time.Minute,
	}
	if cfg.UpstreamURL == "" {
		return GatewayConfig{}, fmt.Errorf("BOSUN_GATEWAY_UPSTREAM_URL is required")
	}
	endpoint, err := url.Parse(cfg.UpstreamURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return GatewayConfig{}, fmt.Errorf("BOSUN_GATEWAY_UPSTREAM_URL must be an HTTPS base URL without credentials, query, or fragment")
	}
	if cfg.UpstreamAPIKey == "" {
		return GatewayConfig{}, fmt.Errorf("BOSUN_GATEWAY_UPSTREAM_API_KEY is required")
	}
	if strings.ContainsAny(cfg.UpstreamAPIKey, "\r\n") {
		return GatewayConfig{}, fmt.Errorf("BOSUN_GATEWAY_UPSTREAM_API_KEY contains invalid characters")
	}
	if !providerNamePattern.MatchString(cfg.Provider) {
		return GatewayConfig{}, fmt.Errorf("BOSUN_GATEWAY_PROVIDER must be a lowercase DNS label")
	}
	switch cfg.UpstreamAuthHeader {
	case "Authorization":
		if cfg.UpstreamAuthScheme == "" {
			cfg.UpstreamAuthScheme = "Bearer"
		}
	case "X-Api-Key":
		if cfg.UpstreamAuthScheme != "" {
			return GatewayConfig{}, fmt.Errorf("BOSUN_GATEWAY_UPSTREAM_AUTH_SCHEME must be empty for X-Api-Key")
		}
	default:
		return GatewayConfig{}, fmt.Errorf("BOSUN_GATEWAY_UPSTREAM_AUTH_HEADER must be Authorization or X-Api-Key")
	}
	if cfg.UpstreamAuthScheme != "" && !authSchemePattern.MatchString(cfg.UpstreamAuthScheme) {
		return GatewayConfig{}, fmt.Errorf("BOSUN_GATEWAY_UPSTREAM_AUTH_SCHEME is invalid")
	}
	if cfg.TokenReviewTimeout, err = duration("BOSUN_GATEWAY_TOKEN_REVIEW_TIMEOUT", cfg.TokenReviewTimeout); err != nil {
		return GatewayConfig{}, err
	}
	if cfg.SessionLookupTimeout, err = duration("BOSUN_GATEWAY_SESSION_LOOKUP_TIMEOUT", cfg.SessionLookupTimeout); err != nil {
		return GatewayConfig{}, err
	}
	if cfg.UpstreamTimeout, err = duration("BOSUN_GATEWAY_UPSTREAM_TIMEOUT", cfg.UpstreamTimeout); err != nil {
		return GatewayConfig{}, err
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
