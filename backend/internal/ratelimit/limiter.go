// Package ratelimit 提供进程内、有界、短窗口的固定窗口限流器（techspec §3.2）。
// 状态仅存单个 backend API 进程内存中，进程重启即丢失，不作为业务权威；P0/P1 不引入 Redis。
package ratelimit

import (
	"sync"
	"time"
)

// Limiter 是按 key 的固定窗口计数器，并对 key 总数设上限。达到上限且无可回收过期项时，
// 对新 key 一律拒绝（spec/techspec 要求「不得放开」），而非淘汰在用条目或放行。
type Limiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	capacity int
	entries  map[string]*window
}

type window struct {
	count   int
	resetAt time.Time
}

// New 构造 limiter：limit 为窗口内允许的最大请求数，window 为窗口时长，capacity 为最大 key 数。
func New(limit int, w time.Duration, capacity int) *Limiter {
	return &Limiter{
		limit:    limit,
		window:   w,
		capacity: capacity,
		entries:  make(map[string]*window),
	}
}

// Allow 记录一次针对 key 的尝试并返回是否放行。达到容量上限且无过期项可回收时返回 false。
func (l *Limiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if entry, ok := l.entries[key]; ok {
		if !now.Before(entry.resetAt) {
			entry.count = 1
			entry.resetAt = now.Add(l.window)
			return true
		}
		if entry.count >= l.limit {
			return false
		}
		entry.count++
		return true
	}

	if len(l.entries) >= l.capacity {
		l.evictExpired(now)
		if len(l.entries) >= l.capacity {
			return false
		}
	}
	l.entries[key] = &window{count: 1, resetAt: now.Add(l.window)}
	return true
}

// Cleanup 移除所有已过期的窗口，可由后台定时调用以约束内存占用。
func (l *Limiter) Cleanup(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evictExpired(now)
}

// Len 返回当前活跃 key 数，供测试与指标使用。
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func (l *Limiter) evictExpired(now time.Time) {
	for key, entry := range l.entries {
		if !now.Before(entry.resetAt) {
			delete(l.entries, key)
		}
	}
}
