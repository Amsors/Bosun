package auth

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"
)

func newTestIssuer(t *testing.T) *JWTIssuer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	issuer, err := NewJWTIssuer("bosun", priv, 15*time.Minute)
	if err != nil {
		t.Fatalf("NewJWTIssuer() error = %v", err)
	}
	return issuer
}

func TestJWTSignVerifyRoundTrip(t *testing.T) {
	issuer := newTestIssuer(t)
	now := time.Unix(1_700_000_000, 0)

	token, err := issuer.Sign("018f9c6e-0000-7000-8000-000000000000", now)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	claims, err := issuer.Verify(token, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if claims.UserID != "018f9c6e-0000-7000-8000-000000000000" {
		t.Fatalf("UserID = %q", claims.UserID)
	}
	if claims.TokenID == "" {
		t.Fatal("TokenID empty, want a jti")
	}
}

func TestJWTVerifyExpired(t *testing.T) {
	issuer := newTestIssuer(t)
	now := time.Unix(1_700_000_000, 0)
	token, err := issuer.Sign("018f9c6e-0000-7000-8000-000000000000", now)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	_, err = issuer.Verify(token, now.Add(16*time.Minute))
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("Verify() error = %v, want ErrTokenExpired", err)
	}
}

func TestJWTVerifyRejectsTamperedAndForeign(t *testing.T) {
	issuer := newTestIssuer(t)
	other := newTestIssuer(t)
	now := time.Unix(1_700_000_000, 0)

	token, err := issuer.Sign("018f9c6e-0000-7000-8000-000000000000", now)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	// 另一密钥签发的 token 不被本 issuer 接受（kid 未知）。
	foreign, err := other.Sign("018f9c6e-0000-7000-8000-000000000000", now)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if _, err := issuer.Verify(foreign, now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("Verify(foreign) error = %v, want ErrTokenInvalid", err)
	}

	// 篡改 payload 使签名失效。
	parts := strings.Split(token, ".")
	tampered := parts[0] + "." + parts[1] + "x." + parts[2]
	if _, err := issuer.Verify(tampered, now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("Verify(tampered) error = %v, want ErrTokenInvalid", err)
	}

	for _, bad := range []string{"", "a.b", "a.b.c.d", "only-one-part"} {
		if _, err := issuer.Verify(bad, now); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("Verify(%q) error = %v, want ErrTokenInvalid", bad, err)
		}
	}
}

func TestNewJWTIssuerRejectsBadKey(t *testing.T) {
	if _, err := NewJWTIssuer("bosun", ed25519.PrivateKey{1, 2, 3}, time.Minute); err == nil {
		t.Fatal("NewJWTIssuer() expected error for short key")
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	if _, err := NewJWTIssuer("bosun", priv, 0); err == nil {
		t.Fatal("NewJWTIssuer() expected error for non-positive ttl")
	}
}
