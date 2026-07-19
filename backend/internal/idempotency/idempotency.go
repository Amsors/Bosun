// Package idempotency 提供创建类接口的幂等键决策与请求指纹计算（spec/03 §5、techspec §6.1）。
// 决策逻辑与存储解耦，便于单测；实际读写在 auth store 层通过 sqlc 完成。
package idempotency

import (
	"bytes"
	"crypto/sha256"
)

// Decision 表示对一次带幂等键请求的处理结论。
type Decision int

const (
	// Proceed 表示无既存记录，应执行业务并存储响应。
	Proceed Decision = iota
	// Replay 表示既存记录且请求指纹一致，应重放存储的首次响应。
	Replay
	// Conflict 表示同一键但请求指纹不同，应返回 10002。
	Conflict
)

// Record 是既存幂等记录的最小视图。
type Record struct {
	RequestHash []byte
	Status      int
	Body        []byte
}

// RequestHash 依据方法、路径与请求体计算稳定指纹，用于识别「同键不同请求」。
func RequestHash(method, path string, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return h.Sum(nil)
}

// Decide 根据既存记录（可为 nil）与当前请求指纹给出处理结论。
func Decide(existing *Record, requestHash []byte) Decision {
	if existing == nil {
		return Proceed
	}
	if bytes.Equal(existing.RequestHash, requestHash) {
		return Replay
	}
	return Conflict
}
