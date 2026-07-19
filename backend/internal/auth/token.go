package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// refreshTokenBytes 是 refresh token 的原始熵长度：256-bit（techspec §7.3）。
const refreshTokenBytes = 32

// NewRefreshToken 生成 256-bit 高熵 refresh token，返回明文与其 SHA-256。
// 明文只经 HttpOnly cookie 下发给客户端，数据库仅存 hash。
func NewRefreshToken() (raw string, hash []byte, err error) {
	buf := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("generate refresh token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, HashRefreshToken(raw), nil
}

// HashRefreshToken 计算 refresh token 明文的 SHA-256，用于比对与存储。
func HashRefreshToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}
