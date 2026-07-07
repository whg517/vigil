package servicesync

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/service"
	"github.com/kevin/vigil/ent/team"
	"github.com/kevin/vigil/internal/metrics"
)

// Syncer 主动同步引擎：拉取期望清单 → upsert source=auto 服务。
type Syncer struct {
	db          *ent.Client
	source      Source
	defaultTeam string           // 兜底团队 slug（清单条目未给 team 时用）
	now         func() time.Time // 可注入（测试用）
}

// NewSyncer 构造同步引擎。
func NewSyncer(db *ent.Client, source Source, defaultTeam string) *Syncer {
	return &Syncer{db: db, source: source, defaultTeam: defaultTeam, now: time.Now}
}

// Result 一次同步的结果统计。
type Result struct {
	Created int
	Updated int
	Skipped int
}

// outcome 单条调和结果（与 metrics result 维度一致）。
type outcome string

const (
	outcomeCreated outcome = "created"
	outcomeUpdated outcome = "updated"
	outcomeSkipped outcome = "skipped"
)

// Reconcile 拉取期望清单并调和：新建/更新 auto 服务，跳过无法解析或命中 manual 的条目。
// 单条失败不中断整批（记日志 + 计 skipped），保证一条脏数据不拖垮整轮同步。
func (s *Syncer) Reconcile(ctx context.Context) (Result, error) {
	desired, err := s.source.List(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list desired services: %w", err)
	}
	var res Result
	for _, d := range desired {
		oc, oerr := s.reconcileOne(ctx, d)
		if oerr != nil {
			slog.Warn("service sync: reconcile item failed", "slug", d.Slug, "error", oerr)
			oc = outcomeSkipped
		}
		switch oc {
		case outcomeCreated:
			res.Created++
		case outcomeUpdated:
			res.Updated++
		default:
			res.Skipped++
		}
		metrics.ServicesSynced.WithLabelValues(string(oc)).Inc()
	}
	return res, nil
}

// reconcileOne 调和单条期望服务。返回其结果类型。
//
// 规则（与懒供给 P1 一致的安全底线）：
//   - 无 slug / 团队解析不到 / 团队无默认升级策略 → skipped（不制造无策略静默服务）。
//   - slug 不存在 → 新建 source=auto。
//   - slug 存在且 source=auto → 更新其 name/labels/team/策略对齐清单。
//   - slug 存在且 source=manual → skipped（尊重人工，绝不覆盖）。
func (s *Syncer) reconcileOne(ctx context.Context, d DesiredService) (outcome, error) {
	if d.Slug == "" {
		return outcomeSkipped, nil
	}
	tm, err := s.resolveTeam(ctx, d.Team)
	if err != nil {
		return outcomeSkipped, err
	}
	if tm == nil {
		return outcomeSkipped, nil
	}
	policy, err := tm.QueryDefaultEscalationPolicy().Only(ctx)
	if ent.IsNotFound(err) {
		return outcomeSkipped, nil // 团队无默认策略 → 不同步（避免无策略静默）
	}
	if err != nil {
		return outcomeSkipped, err
	}

	name := d.Name
	if name == "" {
		name = d.Slug
	}
	labels := d.Labels
	if labels == nil {
		labels = map[string]string{}
	}

	existing, err := s.db.Service.Query().Where(service.SlugEQ(d.Slug)).Only(ctx)
	switch {
	case ent.IsNotFound(err):
		_, cerr := s.db.Service.Create().
			SetName(name).
			SetSlug(d.Slug).
			SetLabels(labels).
			SetSource(service.SourceAuto).
			SetProvisionedAt(s.now()).
			SetStatus(service.StatusActive).
			SetAutoCreateIncident(true).
			SetTeamID(tm.ID).
			SetEscalationPolicyID(policy.ID).
			Save(ctx)
		if cerr != nil {
			// slug 唯一冲突（并发/同轮重复）→ 跳过，下轮同步再对齐。
			if ent.IsConstraintError(cerr) {
				return outcomeSkipped, nil
			}
			return outcomeSkipped, fmt.Errorf("create service %q: %w", d.Slug, cerr)
		}
		return outcomeCreated, nil

	case err != nil:
		return outcomeSkipped, fmt.Errorf("query service %q: %w", d.Slug, err)

	default:
		// 已存在：只更新 auto 服务，绝不触碰 manual（含人工转正过的）。
		if existing.Source != service.SourceAuto {
			return outcomeSkipped, nil
		}
		if _, uerr := s.db.Service.UpdateOneID(existing.ID).
			SetName(name).
			SetLabels(labels).
			SetTeamID(tm.ID).
			SetEscalationPolicyID(policy.ID).
			Save(ctx); uerr != nil {
			return outcomeSkipped, fmt.Errorf("update service %q: %w", d.Slug, uerr)
		}
		return outcomeUpdated, nil
	}
}

// resolveTeam 解析归属团队：清单条目 team slug 优先，缺省用兜底团队；都解析不到返回 (nil,nil)。
func (s *Syncer) resolveTeam(ctx context.Context, teamSlug string) (*ent.Team, error) {
	slug := teamSlug
	if slug == "" {
		slug = s.defaultTeam
	}
	if slug == "" {
		return nil, nil
	}
	tm, err := s.db.Team.Query().Where(team.SlugEQ(slug)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query team %q: %w", slug, err)
	}
	return tm, nil
}

// Run 周期同步循环，ctx 取消时退出（纳入优雅关闭）。装配层 go s.Run(ctx, interval) 启动。
// 参照 analytics.Snapshotter.Run：首轮在 interval 后触发，错误不中断循环。
func (s *Syncer) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if res, err := s.Reconcile(ctx); err != nil {
				slog.Warn("service sync failed", "error", err)
			} else if res.Created > 0 || res.Updated > 0 {
				slog.Info("service sync",
					"created", res.Created, "updated", res.Updated, "skipped", res.Skipped)
			}
		}
	}
}
