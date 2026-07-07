package servicesync

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/event"
	"github.com/kevin/vigil/ent/service"
	"github.com/kevin/vigil/internal/metrics"
)

// Pruner 自动供给服务过期清理（方案C 治理，02-triage-routing §3.5）。
// 把「source=auto 且 StaleDays 天内无新 Event」的服务停用，防长尾泛滥。
type Pruner struct {
	db       *ent.Client
	staleDur time.Duration
	now      func() time.Time // 可注入（测试用）
}

// NewPruner 构造清理器。staleDays<=0 回退 30 天。
func NewPruner(db *ent.Client, staleDays int) *Pruner {
	if staleDays <= 0 {
		staleDays = 30
	}
	return &Pruner{
		db:       db,
		staleDur: time.Duration(staleDays) * 24 * time.Hour,
		now:      time.Now,
	}
}

// Prune 停用所有过期的 auto 服务，返回停用数。
//
// 判定「过期」需同时满足（缺一不可，避免误伤）：
//   - source=auto（绝不触碰 manual/人工转正过的）；
//   - status=active（已停用的跳过，幂等）；
//   - provisioned_at < cutoff（供给早于窗口）——保护刚被主动同步建出、尚无告警的新服务；
//   - 无任何 received_at >= cutoff 的 Event（窗口内确实没新告警）。
//
// 只 disable 不 delete：保留历史 Incident 关联，人工可重新启用/转正。
func (p *Pruner) Prune(ctx context.Context) (int, error) {
	cutoff := p.now().Add(-p.staleDur)
	stale, err := p.db.Service.Query().
		Where(
			service.SourceEQ(service.SourceAuto),
			service.StatusEQ(service.StatusActive),
			service.ProvisionedAtLT(cutoff),
			service.Not(service.HasEventsWith(event.ReceivedAtGTE(cutoff))),
		).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("query stale services: %w", err)
	}
	if len(stale) == 0 {
		return 0, nil
	}
	ids := make([]int, len(stale))
	slugs := make([]string, len(stale))
	for i, s := range stale {
		ids[i] = s.ID
		slugs[i] = s.Slug
	}
	n, err := p.db.Service.Update().
		Where(service.IDIn(ids...)).
		SetStatus(service.StatusDisabled).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("disable stale services: %w", err)
	}
	metrics.ServicesPruned.Add(float64(n))
	slog.Info("service cleanup: disabled stale auto services", "count", n, "slugs", slugs)
	return n, nil
}

// Run 周期清理循环，ctx 取消时退出（纳入优雅关闭）。装配层 go p.Run(ctx, interval) 启动。
func (p *Pruner) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := p.Prune(ctx); err != nil {
				slog.Warn("service cleanup failed", "error", err)
			}
		}
	}
}
