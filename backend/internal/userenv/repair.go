package userenv

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// UserLister 提供修复循环所需的用户 ID 来源，由 auth store 实现。
type UserLister interface {
	ListUserIDs(ctx context.Context) ([]string, error)
}

// Repairer 周期性补建注册后未成功创建的 UserEnvironment CR（techspec §5.1）。
// DB 写入与 CR 创建无分布式事务，修复循环是正确性的一部分而非可选优化。
type Repairer struct {
	lister   UserLister
	prov     Provisioner
	interval time.Duration
	logger   *slog.Logger
}

// NewRepairer 构造修复循环。interval 为扫描周期。
func NewRepairer(lister UserLister, prov Provisioner, interval time.Duration, logger *slog.Logger) *Repairer {
	return &Repairer{lister: lister, prov: prov, interval: interval, logger: logger}
}

// Run 阻塞运行修复循环直到 ctx 取消，适合在独立 goroutine 中启动。
func (r *Repairer) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil {
				r.logger.Error("user environment repair sweep failed", "reason", err)
			}
		}
	}
}

// RunOnce 执行一轮补建：对缺失 CR 的用户调用 Ensure，返回首个致命错误。
func (r *Repairer) RunOnce(ctx context.Context) error {
	userIDs, err := r.lister.ListUserIDs(ctx)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	existing, err := r.prov.ExistingUserIDs(ctx)
	if err != nil {
		return fmt.Errorf("list existing environments: %w", err)
	}
	repaired := 0
	for _, userID := range userIDs {
		if _, ok := existing[userID]; ok {
			continue
		}
		if err := r.prov.Ensure(ctx, userID); err != nil {
			return fmt.Errorf("ensure environment for %s: %w", userID, err)
		}
		repaired++
	}
	if repaired > 0 {
		r.logger.Info("user environment repair sweep completed", "reason", "repaired_missing", "count", repaired)
	}
	return nil
}
