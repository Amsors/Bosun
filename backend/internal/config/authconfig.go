package config

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Amsors/Bosun/backend/internal/auth"
)

// AuthConfig 汇总 backend API 的认证配置（techspec §7.3）。仅 api 组件加载。
type AuthConfig struct {
	JWTPrivateKey   ed25519.PrivateKey
	Issuer          string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	Argon2          auth.Argon2Params

	RefreshCookieName   string
	RefreshCookiePath   string
	RefreshCookieSecure bool

	LoginIPLimit       int
	LoginIPWindow      time.Duration
	LoginEmailLimit    int
	LoginEmailWindow   time.Duration
	LoginLimiterCap    int
	TrustedProxyHeader string

	RepairInterval time.Duration
}

// LoadAuth 解析 API 认证配置。JWT 私钥必须由集群外注入（spec/05），缺失即报错。
func LoadAuth() (AuthConfig, error) {
	cfg := AuthConfig{
		Issuer:              valueOrDefault("BOSUN_API_JWT_ISSUER", "bosun"),
		AccessTokenTTL:      15 * time.Minute,
		RefreshTokenTTL:     14 * 24 * time.Hour,
		Argon2:              auth.DefaultArgon2Params(),
		RefreshCookieName:   valueOrDefault("BOSUN_API_REFRESH_COOKIE_NAME", "bosun_refresh"),
		RefreshCookiePath:   valueOrDefault("BOSUN_API_REFRESH_COOKIE_PATH", "/api/v1/auth"),
		RefreshCookieSecure: true,
		LoginIPLimit:        10,
		LoginIPWindow:       time.Minute,
		LoginEmailLimit:     5,
		LoginEmailWindow:    15 * time.Minute,
		LoginLimiterCap:     10000,
		TrustedProxyHeader:  os.Getenv("BOSUN_API_TRUSTED_PROXY_HEADER"),
		RepairInterval:      5 * time.Minute,
	}

	key, err := loadEd25519PrivateKey()
	if err != nil {
		return AuthConfig{}, err
	}
	cfg.JWTPrivateKey = key

	if cfg.AccessTokenTTL, err = duration("BOSUN_API_ACCESS_TOKEN_TTL", cfg.AccessTokenTTL); err != nil {
		return AuthConfig{}, err
	}
	if cfg.RefreshTokenTTL, err = duration("BOSUN_API_REFRESH_TOKEN_TTL", cfg.RefreshTokenTTL); err != nil {
		return AuthConfig{}, err
	}
	if cfg.LoginIPWindow, err = duration("BOSUN_API_LOGIN_IP_WINDOW", cfg.LoginIPWindow); err != nil {
		return AuthConfig{}, err
	}
	if cfg.LoginEmailWindow, err = duration("BOSUN_API_LOGIN_EMAIL_WINDOW", cfg.LoginEmailWindow); err != nil {
		return AuthConfig{}, err
	}
	if cfg.RepairInterval, err = duration("BOSUN_API_REPAIR_INTERVAL", cfg.RepairInterval); err != nil {
		return AuthConfig{}, err
	}
	if cfg.LoginIPLimit, err = positiveInt("BOSUN_API_LOGIN_IP_LIMIT", cfg.LoginIPLimit); err != nil {
		return AuthConfig{}, err
	}
	if cfg.LoginEmailLimit, err = positiveInt("BOSUN_API_LOGIN_EMAIL_LIMIT", cfg.LoginEmailLimit); err != nil {
		return AuthConfig{}, err
	}
	if cfg.LoginLimiterCap, err = positiveInt("BOSUN_API_LOGIN_LIMITER_CAPACITY", cfg.LoginLimiterCap); err != nil {
		return AuthConfig{}, err
	}
	if cfg.RefreshCookieSecure, err = boolValue("BOSUN_API_REFRESH_COOKIE_SECURE", true); err != nil {
		return AuthConfig{}, err
	}
	return cfg, nil
}

func loadEd25519PrivateKey() (ed25519.PrivateKey, error) {
	raw := os.Getenv("BOSUN_API_JWT_PRIVATE_KEY")
	if raw == "" {
		if path := os.Getenv("BOSUN_API_JWT_PRIVATE_KEY_FILE"); path != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read BOSUN_API_JWT_PRIVATE_KEY_FILE: %w", err)
			}
			raw = string(data)
		}
	}
	if raw == "" {
		return nil, fmt.Errorf("BOSUN_API_JWT_PRIVATE_KEY or BOSUN_API_JWT_PRIVATE_KEY_FILE is required")
	}
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, fmt.Errorf("JWT private key is not valid PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse JWT private key (want PKCS#8 Ed25519): %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("JWT private key is %T, want ed25519.PrivateKey", parsed)
	}
	return key, nil
}

func positiveInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return value, nil
}

func boolValue(key string, fallback bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return value, nil
}
