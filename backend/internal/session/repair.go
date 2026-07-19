package session

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Repairer closes the DB/CR distributed-transaction gap for create and delete.
type Repairer struct {
	store    Store
	runtime  RuntimeControl
	interval time.Duration
	logger   *slog.Logger
}

func NewRepairer(store Store, runtime RuntimeControl, interval time.Duration, logger *slog.Logger) *Repairer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Repairer{store: store, runtime: runtime, interval: interval, logger: logger}
}

func (r *Repairer) Run(ctx context.Context) {
	if err := r.RunOnce(ctx); err != nil {
		r.logger.Error("session repair sweep failed", "reason", err)
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil {
				r.logger.Error("session repair sweep failed", "reason", err)
			}
		}
	}
}

func (r *Repairer) RunOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pending, err := r.store.ListPending(ctx, 100)
	if err != nil {
		return err
	}
	for _, rec := range pending {
		if err := r.runtime.Ensure(ctx, rec); err != nil {
			return fmt.Errorf("repair missing AgentSession %s: %w", rec.ID, err)
		}
	}
	deleting, err := r.store.ListDeleting(ctx, 100)
	if err != nil {
		return err
	}
	for _, rec := range deleting {
		if err := r.runtime.Delete(ctx, rec); err != nil {
			return fmt.Errorf("repair AgentSession deletion %s: %w", rec.ID, err)
		}
	}
	return nil
}
