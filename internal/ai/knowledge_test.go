// knowledge_test.go T3.4/B18：相似复盘知识检索修复。
//
// 覆盖三条 B18 修复：
//   - archived 复盘仍参与检索（不再硬编码 status='published'）；
//   - pgvector 不可用时 similar-postmortems 走 LIKE 文本降级（不静默返回 []）；
//   - 发布时 embedding 静默失败留下的空洞，在检索路径被懒补算（检索库最终一致）。
package ai

import (
	"context"
	"database/sql"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	"github.com/kevin/vigil/ent/incident"
	"github.com/kevin/vigil/ent/postmortem"

	_ "github.com/mattn/go-sqlite3"
)

// seedIncidentWithPostmortem 建一条 incident + 关联复盘（指定状态）。
// title/summary 用于 LIKE 降级匹配的关键词来源。
func seedIncidentWithPostmortem(t *testing.T, c *ent.Client, num, title string, st postmortem.Status) (*ent.Incident, *ent.Postmortem) {
	t.Helper()
	ctx := context.Background()
	inc, err := c.Incident.Create().
		SetNumber(num).SetTitle(title).SetSeverity(incident.SeverityCritical).
		SetStatus(incident.StatusResolved).SetPriority(incident.PriorityP1).
		SetSummary(title).SetTriggerType(incident.TriggerTypeAuto).Save(ctx)
	if err != nil {
		t.Fatalf("create incident %s: %v", num, err)
	}
	pm, err := c.Postmortem.Create().
		SetIncidentID(inc.ID).SetStatus(st).SetGeneratedBy(postmortem.GeneratedByHuman).
		SetSections(map[string]any{"summary": title}).Save(ctx)
	if err != nil {
		t.Fatalf("create postmortem for %s: %v", num, err)
	}
	return inc, pm
}

// TestFindSimilarPostmortems_ArchivedIncludedViaLIKE B18①③：
// pgvector 不可用（无 runSQL）时走 LIKE 降级，且 archived 复盘仍被命中（不被状态硬编码排除）。
func TestFindSimilarPostmortems_ArchivedIncludedViaLIKE(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:sim_pm_archived?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	// 查询源 incident：标题含 "支付"
	query, _ := c.Incident.Create().
		SetNumber("INC-Q").SetTitle("支付网关超时").SetSeverity(incident.SeverityCritical).
		SetStatus(incident.StatusTriggered).SetPriority(incident.PriorityP1).
		SetSummary("支付网关超时").SetTriggerType(incident.TriggerTypeAuto).Save(ctx)

	// 历史复盘：一条 published（"支付"）、一条 archived（"支付"）、一条无关（"网络"）
	seedIncidentWithPostmortem(t, c, "INC-P", "支付服务5xx", postmortem.StatusPublished)
	seedIncidentWithPostmortem(t, c, "INC-A", "支付超时排查", postmortem.StatusArchived)
	seedIncidentWithPostmortem(t, c, "INC-N", "网络抖动", postmortem.StatusPublished)

	// 无 provider + 无 runSQL → 走 LIKE 降级（不静默返回 []）。
	e := NewDiagnoseEngine(c, nil)
	pms, err := e.FindSimilarPostmortems(ctx, query.ID, 5)
	if err != nil {
		t.Fatalf("FindSimilarPostmortems: %v", err)
	}
	if len(pms) == 0 {
		t.Fatal("B18：pgvector 不可用时 similar-postmortems 应 LIKE 降级返回非空，而非静默 []")
	}
	// 应命中 published 与 archived（都含 "支付"关键词），不含 "网络"。
	var foundPublished, foundArchived, foundNetwork bool
	for _, pm := range pms {
		switch postmortem.Status(pm.Status) {
		case postmortem.StatusPublished:
			if pm.Edges.Incident != nil && pm.Edges.Incident.Number == "INC-P" {
				foundPublished = true
			}
		case postmortem.StatusArchived:
			foundArchived = true
		}
		if pm.Edges.Incident != nil && pm.Edges.Incident.Number == "INC-N" {
			foundNetwork = true
		}
	}
	if !foundPublished {
		t.Error("应命中 published 复盘（INC-P，含支付关键词）")
	}
	if !foundArchived {
		t.Error("B18：archived 复盘应仍参与检索（含支付关键词），不被状态硬编码排除")
	}
	if foundNetwork {
		t.Error("不相关复盘（网络）不应命中")
	}
}

// TestFindSimilarPostmortems_LIKEExcludesSelfAndDraft LIKE 降级应排除查询源自身的复盘，
// 且只取 published/archived（draft/in_review 未定稿不算有效知识）。
func TestFindSimilarPostmortems_LIKEExcludesSelfAndDraft(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:sim_pm_self?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	// 查询源自身也有复盘（published），关键词与自己匹配——但应被 IDNEQ 排除。
	query, self := seedIncidentWithPostmortem(t, c, "INC-SELF", "磁盘写满告警", postmortem.StatusPublished)
	// 一条 draft（关键词匹配但未定稿）→ 不应命中
	seedIncidentWithPostmortem(t, c, "INC-DRAFT", "磁盘清理", postmortem.StatusDraft)
	// 一条 published（关键词匹配）→ 应命中
	_, want := seedIncidentWithPostmortem(t, c, "INC-OK", "磁盘扩容", postmortem.StatusPublished)

	e := NewDiagnoseEngine(c, nil)
	pms, err := e.FindSimilarPostmortems(ctx, query.ID, 5)
	if err != nil {
		t.Fatalf("FindSimilarPostmortems: %v", err)
	}
	for _, pm := range pms {
		if pm.ID == self.ID {
			t.Error("LIKE 降级不应返回查询源自身的复盘")
		}
		if postmortem.Status(pm.Status) == postmortem.StatusDraft {
			t.Error("draft 复盘（未定稿）不应参与知识检索")
		}
	}
	var foundWant bool
	for _, pm := range pms {
		if pm.ID == want.ID {
			foundWant = true
		}
	}
	if !foundWant {
		t.Error("应命中匹配的 published 复盘 INC-OK")
	}
}

// hasEmbedding 判定复盘是否已有有效 embedding。
// 注意：sqlite 下 embedding 列存 blob，未设时读回是非 nil 的 NullableVector{Valid:false}
// （非 SQL NULL），故不能用 Embedding==nil 判定，须看 .Valid。postgres 下未设为真 NULL。
func hasEmbedding(pm *ent.Postmortem) bool {
	return pm.Embedding != nil && pm.Embedding.Valid
}

// TestBackfillPostmortemEmbeddings B18②：published/archived 却缺 embedding 的复盘，
// 在检索路径被懒补算（provider+runSQL 可用时）。
func TestBackfillPostmortemEmbeddings(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:pm_backfill?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	// 一条 published 复盘，embedding 未算（模拟发布时 embedding 计算静默失败）。
	_, pmMissing := seedIncidentWithPostmortem(t, c, "INC-MISS", "缓存击穿", postmortem.StatusPublished)
	if hasEmbedding(pmMissing) {
		t.Fatal("前置：新建复盘不应有有效 embedding")
	}
	// 一条 draft（不在补算范围）——确保补算只碰 published/archived。
	_, pmDraft := seedIncidentWithPostmortem(t, c, "INC-DRFT", "限流误伤", postmortem.StatusDraft)

	// provider 可用（返回固定向量）+ runSQL 可用（走 pgvector 路径不降级）。
	mp := &mockProvider{resp: "x", avail: true}
	e := NewDiagnoseEngine(c, mp)
	e.SetSQLRunner(func(_ context.Context, _ string, _ []any, _ func(*sql.Rows) error) error {
		return nil // pgvector 命中 0 条即可，本用例只验证补算副作用
	})

	// 触发检索（内部会 backfill）。查询源随便建一条。
	query, _ := c.Incident.Create().
		SetNumber("INC-QB").SetTitle("缓存").SetSeverity(incident.SeverityCritical).
		SetStatus(incident.StatusTriggered).SetPriority(incident.PriorityP1).
		SetSummary("缓存").SetTriggerType(incident.TriggerTypeAuto).Save(ctx)
	if _, err := e.FindSimilarPostmortems(ctx, query.ID, 5); err != nil {
		t.Fatalf("FindSimilarPostmortems: %v", err)
	}

	// published 缺 embedding 的应已被补算（有有效向量）。
	got, _ := c.Postmortem.Get(ctx, pmMissing.ID)
	if !hasEmbedding(got) {
		t.Error("B18：published 缺 embedding 的复盘应在检索路径被懒补算")
	}
	// draft 的不在补算范围，仍无有效 embedding。
	gotDraft, _ := c.Postmortem.Get(ctx, pmDraft.ID)
	if hasEmbedding(gotDraft) {
		t.Error("draft 复盘不应被补算 embedding（不参与知识检索）")
	}
}

// TestBackfillPostmortemEmbeddings_SkipWithoutProvider provider 不可用时不补算
// （检索走 LIKE 降级，补算 embedding 无意义，避免无效调用）。
func TestBackfillPostmortemEmbeddings_SkipWithoutProvider(t *testing.T) {
	c := enttest.Open(t, "sqlite3", "file:pm_backfill_skip?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	_, pm := seedIncidentWithPostmortem(t, c, "INC-NP", "队列堆积", postmortem.StatusPublished)

	e := NewDiagnoseEngine(c, nil) // 无 provider
	e.backfillPostmortemEmbeddings(ctx)

	got, _ := c.Postmortem.Get(ctx, pm.ID)
	if hasEmbedding(got) {
		t.Error("无 provider 时不应补算 embedding（检索走 LIKE 降级）")
	}
}
