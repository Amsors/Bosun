// Package userenv 负责 backend 侧的 UserEnvironment CR 幂等创建与修复循环（techspec §4.1、§5.1）。
package userenv

import (
	"crypto/sha256"
	"encoding/hex"
)

// shortIDLength 是用户短 ID 的 hex 长度，满足 UserEnvironment.spec.namespace 的
// `^bosun-u-[a-z0-9]{8,16}$` 约束，且由 userID 的 SHA-256 派生以避免 UUID v7 时间戳前缀碰撞、不暴露原 UUID。
const shortIDLength = 12

// ShortID 从用户 UUID 派生稳定的短 ID（12 位小写 hex）。
func ShortID(userID string) string {
	sum := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(sum[:])[:shortIDLength]
}

// Namespace 返回该用户的隔离命名空间名。
func Namespace(userID string) string {
	return "bosun-u-" + ShortID(userID)
}

// CRName 返回该用户 UserEnvironment CR 的名称（cluster-scoped）。
func CRName(userID string) string {
	return "usr-" + ShortID(userID)
}
