package terminal

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

type activityState struct {
	last   time.Time
	active bool
}

type activityLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	states   map[uuid.UUID]activityState
}

func newActivityLimiter(interval time.Duration) *activityLimiter {
	return &activityLimiter{interval: interval, states: make(map[uuid.UUID]activityState)}
}

func (l *activityLimiter) mark(
	ctx context.Context,
	target Target,
	at time.Time,
	update func(context.Context, Target, time.Time) error,
) error {
	l.mu.Lock()
	state := l.states[target.SessionID]
	if state.active || (!state.last.IsZero() && at.Sub(state.last) < l.interval) {
		l.mu.Unlock()
		return nil
	}
	state.active = true
	l.states[target.SessionID] = state
	l.mu.Unlock()

	err := update(ctx, target, at)
	l.mu.Lock()
	state = l.states[target.SessionID]
	state.active = false
	if err == nil && at.After(state.last) {
		state.last = at
	}
	l.states[target.SessionID] = state
	l.mu.Unlock()
	return err
}
