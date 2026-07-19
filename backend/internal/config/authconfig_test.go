package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func testKeyPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BOSUN_API_JWT_PRIVATE_KEY", "BOSUN_API_JWT_PRIVATE_KEY_FILE", "BOSUN_API_JWT_ISSUER",
		"BOSUN_API_ACCESS_TOKEN_TTL", "BOSUN_API_REFRESH_TOKEN_TTL", "BOSUN_API_LOGIN_IP_LIMIT",
		"BOSUN_API_LOGIN_IP_WINDOW", "BOSUN_API_LOGIN_EMAIL_LIMIT", "BOSUN_API_LOGIN_EMAIL_WINDOW",
		"BOSUN_API_LOGIN_LIMITER_CAPACITY", "BOSUN_API_REFRESH_COOKIE_SECURE", "BOSUN_API_TRUSTED_PROXY_HEADER",
		"BOSUN_API_REPAIR_INTERVAL",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadAuthRequiresKey(t *testing.T) {
	clearAuthEnv(t)
	if _, err := LoadAuth(); err == nil {
		t.Fatal("LoadAuth() expected error when JWT key is absent")
	}
}

func TestLoadAuthParsesKeyAndDefaults(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("BOSUN_API_JWT_PRIVATE_KEY", testKeyPEM(t))

	cfg, err := LoadAuth()
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if len(cfg.JWTPrivateKey) != ed25519.PrivateKeySize {
		t.Fatalf("private key size = %d", len(cfg.JWTPrivateKey))
	}
	if cfg.AccessTokenTTL != 15*time.Minute {
		t.Fatalf("AccessTokenTTL = %v, want 15m", cfg.AccessTokenTTL)
	}
	if cfg.RefreshTokenTTL != 14*24*time.Hour {
		t.Fatalf("RefreshTokenTTL = %v, want 336h", cfg.RefreshTokenTTL)
	}
	if !cfg.RefreshCookieSecure {
		t.Fatal("RefreshCookieSecure default should be true")
	}
}

func TestLoadAuthRejectsBadKeyAndBadValues(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("BOSUN_API_JWT_PRIVATE_KEY", "not-a-pem")
	if _, err := LoadAuth(); err == nil {
		t.Fatal("LoadAuth() expected error for malformed PEM")
	}

	clearAuthEnv(t)
	t.Setenv("BOSUN_API_JWT_PRIVATE_KEY", testKeyPEM(t))
	t.Setenv("BOSUN_API_LOGIN_IP_LIMIT", "0")
	if _, err := LoadAuth(); err == nil {
		t.Fatal("LoadAuth() expected error for non-positive login limit")
	}
}
