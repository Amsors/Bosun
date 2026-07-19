package ratelimit

import (
	"testing"
	"time"
)

func TestLimiterAllowsUpToLimitThenBlocks(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(3, time.Minute, 100)
	for i := 0; i < 3; i++ {
		if !l.Allow("k", now) {
			t.Fatalf("attempt %d blocked, want allowed", i)
		}
	}
	if l.Allow("k", now) {
		t.Fatal("4th attempt allowed, want blocked")
	}
}

func TestLimiterResetsAfterWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(1, time.Minute, 100)
	if !l.Allow("k", now) {
		t.Fatal("first attempt blocked")
	}
	if l.Allow("k", now.Add(30*time.Second)) {
		t.Fatal("attempt within window allowed, want blocked")
	}
	if !l.Allow("k", now.Add(2*time.Minute)) {
		t.Fatal("attempt after window blocked, want allowed")
	}
}

func TestLimiterResetsAtWindowBoundary(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(1, time.Minute, 100)
	if !l.Allow("k", now) {
		t.Fatal("first attempt blocked")
	}
	if !l.Allow("k", now.Add(time.Minute)) {
		t.Fatal("attempt at exact reset boundary blocked, want allowed")
	}
}

func TestLimiterCapacityRejectsNewKeys(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(5, time.Minute, 2)
	if !l.Allow("a", now) || !l.Allow("b", now) {
		t.Fatal("initial keys within capacity blocked")
	}
	// 容量已满且现有条目未过期：新 key 必须被拒绝，不得淘汰在用条目。
	if l.Allow("c", now) {
		t.Fatal("new key at capacity allowed, want blocked")
	}
	// 已在册的 key 仍可继续计数。
	if !l.Allow("a", now) {
		t.Fatal("existing key blocked at capacity")
	}
}

func TestLimiterCapacityReclaimsExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(5, time.Minute, 2)
	l.Allow("a", now)
	l.Allow("b", now)
	// 窗口过后，过期项应被回收以容纳新 key。
	later := now.Add(2 * time.Minute)
	if !l.Allow("c", later) {
		t.Fatal("new key blocked after expired entries should be reclaimed")
	}
}

func TestLimiterCleanup(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(5, time.Minute, 10)
	l.Allow("a", now)
	l.Allow("b", now)
	if l.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", l.Len())
	}
	l.Cleanup(now.Add(2 * time.Minute))
	if l.Len() != 0 {
		t.Fatalf("Len() after cleanup = %d, want 0", l.Len())
	}
}
