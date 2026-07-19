package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// access token 采用 Ed25519/EdDSA 并携带 kid（techspec §7.3）。这里使用标准库直接实现最小 JWT，
// 避免引入第三方 JWT 库、精确控制 alg/kid/exp 校验，降低依赖面与实现歧义。

var (
	// ErrTokenExpired 表示 token 已过期，handler 据此返回 20002。
	ErrTokenExpired = errors.New("token expired")
	// ErrTokenInvalid 表示签名、结构或声明无效，handler 据此返回 20001。
	ErrTokenInvalid = errors.New("token invalid")
)

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

type jwtClaims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	TokenID   string `json:"jti"`
}

// Claims 是校验成功后返回的最小声明集。
type Claims struct {
	UserID    string
	IssuedAt  time.Time
	ExpiresAt time.Time
	TokenID   string
}

// JWTIssuer 使用固定 Ed25519 私钥签发并校验 access token。P0 单密钥，kid 由公钥派生，
// 校验侧据 kid 选择公钥以便未来平滑轮换。
type JWTIssuer struct {
	issuer string
	ttl    time.Duration
	priv   ed25519.PrivateKey
	kid    string
	pubs   map[string]ed25519.PublicKey
}

// NewJWTIssuer 从 Ed25519 私钥构造签发/校验器。ttl 为 access token 有效期（15 分钟）。
func NewJWTIssuer(issuer string, priv ed25519.PrivateKey, ttl time.Duration) (*JWTIssuer, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("ed25519 private key size = %d, want %d", len(priv), ed25519.PrivateKeySize)
	}
	if ttl <= 0 {
		return nil, errors.New("access token ttl must be positive")
	}
	pub := priv.Public().(ed25519.PublicKey)
	kid := keyID(pub)
	return &JWTIssuer{
		issuer: issuer,
		ttl:    ttl,
		priv:   priv,
		kid:    kid,
		pubs:   map[string]ed25519.PublicKey{kid: pub},
	}, nil
}

// KeyID 返回当前签名公钥的 kid，便于测试与运维核对。
func (s *JWTIssuer) KeyID() string { return s.kid }

// Sign 为给定用户签发 access token。
func (s *JWTIssuer) Sign(userID string, now time.Time) (string, error) {
	header := jwtHeader{Alg: "EdDSA", Typ: "JWT", Kid: s.kid}
	claims := jwtClaims{
		Issuer:    s.issuer,
		Subject:   userID,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(s.ttl).Unix(),
		TokenID:   uuid.NewString(),
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshal jwt header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal jwt claims: %w", err)
	}
	signingInput := encodeSegment(headerJSON) + "." + encodeSegment(claimsJSON)
	signature := ed25519.Sign(s.priv, []byte(signingInput))
	return signingInput + "." + encodeSegment(signature), nil
}

// Verify 校验 token 的签名、issuer 与过期时间，返回声明。过期返回 ErrTokenExpired。
func (s *JWTIssuer) Verify(token string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrTokenInvalid
	}
	headerJSON, err := decodeSegment(parts[0])
	if err != nil {
		return Claims{}, ErrTokenInvalid
	}
	var header jwtHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return Claims{}, ErrTokenInvalid
	}
	if header.Alg != "EdDSA" || header.Typ != "JWT" {
		return Claims{}, ErrTokenInvalid
	}
	pub, ok := s.pubs[header.Kid]
	if !ok {
		return Claims{}, ErrTokenInvalid
	}
	signature, err := decodeSegment(parts[2])
	if err != nil {
		return Claims{}, ErrTokenInvalid
	}
	signingInput := parts[0] + "." + parts[1]
	if !ed25519.Verify(pub, []byte(signingInput), signature) {
		return Claims{}, ErrTokenInvalid
	}
	claimsJSON, err := decodeSegment(parts[1])
	if err != nil {
		return Claims{}, ErrTokenInvalid
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return Claims{}, ErrTokenInvalid
	}
	if claims.Issuer != s.issuer || claims.Subject == "" {
		return Claims{}, ErrTokenInvalid
	}
	if now.Unix() >= claims.ExpiresAt {
		return Claims{}, ErrTokenExpired
	}
	return Claims{
		UserID:    claims.Subject,
		IssuedAt:  time.Unix(claims.IssuedAt, 0).UTC(),
		ExpiresAt: time.Unix(claims.ExpiresAt, 0).UTC(),
		TokenID:   claims.TokenID,
	}, nil
}

func keyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}

func encodeSegment(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeSegment(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
