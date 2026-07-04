// handler_ops_test.go 接入运维端点测试（T5.1）：干跑测试 + token 轮换。
package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"
	entintegration "github.com/kevin/vigil/ent/integration"
	"github.com/kevin/vigil/internal/ingestion"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

func opsEnv(t *testing.T, dsn, integType string) (*Handler, *ent.Client, *ent.Integration) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:"+dsn+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	team, _ := c.Team.Create().SetName("t").SetSlug("t-" + dsn).Save(ctx)
	integ, err := c.Integration.Create().
		SetName("real").SetType(entintegration.Type(integType)).
		SetToken("tok-" + dsn).SetTeamID(team.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	h := NewHandler(c) // authz nil → checkAccess 放行
	h.SetAdapterRegistry(ingestion.NewAdapterRegistry())
	return h, c, integ
}

func TestIntegrationTest_DryRunPreview(t *testing.T) {
	h, _, integ := opsEnv(t, "ops_test", "webhook")
	e := echo.New()
	e.POST("/integrations/:id/test", h.test)

	// 通用 JSON payload：验证归一化预览命中 labels/severity。
	body := `{"payload":{"source_event_id":"e1","severity":"critical","summary":"disk","labels":{"env":"prod"}}}`
	req := httptest.NewRequest(http.MethodPost, "/integrations/"+strconv.Itoa(integ.ID)+"/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp testResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Matched {
		t.Fatalf("matched=false, error=%s", resp.Error)
	}
	if resp.Count != 1 {
		t.Errorf("count=%d, want 1", resp.Count)
	}
	if len(resp.Events) == 1 {
		if resp.Events[0].Severity != "critical" {
			t.Errorf("severity=%s, want critical", resp.Events[0].Severity)
		}
		if resp.Events[0].Labels["env"] != "prod" {
			t.Errorf("labels not surfaced: %v", resp.Events[0].Labels)
		}
	}
}

func TestIntegrationTest_DryRunDoesNotPersist(t *testing.T) {
	h, c, integ := opsEnv(t, "ops_nopersist", "webhook")
	e := echo.New()
	e.POST("/integrations/:id/test", h.test)
	body := `{"payload":{"source_event_id":"e2","summary":"x"}}`
	req := httptest.NewRequest(http.MethodPost, "/integrations/"+strconv.Itoa(integ.ID)+"/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(httptest.NewRecorder(), req)

	// 干跑绝不落 RawEvent/Event。
	if n, _ := c.RawEvent.Query().Count(context.Background()); n != 0 {
		t.Errorf("raw_event count=%d, want 0 (dry-run must not persist)", n)
	}
	if n, _ := c.Event.Query().Count(context.Background()); n != 0 {
		t.Errorf("event count=%d, want 0", n)
	}
}

func TestIntegrationTest_ParseFailedReported(t *testing.T) {
	h, _, integ := opsEnv(t, "ops_parsefail", "prometheus")
	e := echo.New()
	e.POST("/integrations/:id/test", h.test)
	// Prometheus 适配器要求 alerts[]，空对象会解析失败 → matched=false + error。
	body := `{"payload":{"not":"alertmanager"}}`
	req := httptest.NewRequest(http.MethodPost, "/integrations/"+strconv.Itoa(integ.ID)+"/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var resp testResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Matched {
		t.Errorf("matched=true, want false for invalid prometheus payload")
	}
	if resp.Error == "" {
		t.Errorf("error empty, want parse failure reason")
	}
}

func TestRotateToken_OldInvalidated(t *testing.T) {
	h, c, integ := opsEnv(t, "ops_rotate", "webhook")
	oldToken := integ.Token
	e := echo.New()
	e.POST("/integrations/:id/rotate-token", h.rotateToken)

	req := httptest.NewRequest(http.MethodPost, "/integrations/"+strconv.Itoa(integ.ID)+"/rotate-token", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp rotateTokenResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Token == "" || resp.Token == oldToken {
		t.Errorf("new token=%q, want non-empty and != old", resp.Token)
	}
	// DB 里 token 已换新，旧 token 不再匹配。
	got, _ := c.Integration.Get(context.Background(), integ.ID)
	if got.Token == oldToken {
		t.Errorf("db token still old after rotate")
	}
	if got.Token != resp.Token {
		t.Errorf("db token=%q != returned %q", got.Token, resp.Token)
	}
}
