package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/kevin/vigil/ent"
	entservice "github.com/kevin/vigil/ent/service"

	"github.com/labstack/echo/v5"
)

// newSourceHandler 起一个不注入 authz 的 Handler（开放，专测 source 过滤/转正逻辑）。
func newSourceHandler(c *ent.Client) *echo.Echo {
	h := NewHandler(c)
	e := echo.New()
	v1 := e.Group("/api/v1")
	h.Register(v1)
	return e
}

func doReq(e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	var r *strings.Reader
	if body == "" {
		r = strings.NewReader("")
	} else {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func seedSvc(t *testing.T, c *ent.Client, teamID int, slug string, src entservice.Source) *ent.Service {
	t.Helper()
	svc, err := c.Service.Create().
		SetName(slug).SetSlug(slug).SetTeamID(teamID).SetSource(src).
		Save(context.Background())
	if err != nil {
		t.Fatalf("seed service %s: %v", slug, err)
	}
	return svc
}

// TestServiceListSourceFilter ?source=auto|manual 精确过滤，非法值 400。
func TestServiceListSourceFilter(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	tm, _ := c.Team.Create().SetName("t").SetSlug("t").Save(ctx)
	seedSvc(t, c, tm.ID, "manual-svc", entservice.SourceManual)
	seedSvc(t, c, tm.ID, "auto-svc", entservice.SourceAuto)
	e := newSourceHandler(c)

	// source=auto → 只返回自动供给的。
	rec := doReq(e, http.MethodGet, "/api/v1/services?source=auto", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("auto filter status = %d", rec.Code)
	}
	var autoList []struct {
		Slug   string `json:"slug"`
		Source string `json:"source"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &autoList)
	if len(autoList) != 1 || autoList[0].Slug != "auto-svc" || autoList[0].Source != "auto" {
		t.Fatalf("source=auto filter: got %+v, want only auto-svc", autoList)
	}

	// source=manual → 只返回手工的。
	rec = doReq(e, http.MethodGet, "/api/v1/services?source=manual", "")
	var manualList []struct {
		Slug string `json:"slug"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &manualList)
	if len(manualList) != 1 || manualList[0].Slug != "manual-svc" {
		t.Fatalf("source=manual filter: got %+v, want only manual-svc", manualList)
	}

	// 非法值 → 400。
	rec = doReq(e, http.MethodGet, "/api/v1/services?source=bogus", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid source: status = %d, want 400", rec.Code)
	}
}

// TestServiceAdopt 转正：PATCH source=manual 把自动服务标记为手工；source=auto 被拒 400。
func TestServiceAdopt(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	tm, _ := c.Team.Create().SetName("t").SetSlug("t").Save(ctx)
	svc := seedSvc(t, c, tm.ID, "auto-svc", entservice.SourceAuto)
	e := newSourceHandler(c)

	// 转正 → 200，库中 source=manual。
	rec := doReq(e, http.MethodPatch, "/api/v1/services/"+strconv.Itoa(svc.ID), `{"source":"manual"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	got, _ := c.Service.Get(ctx, svc.ID)
	if got.Source != entservice.SourceManual {
		t.Fatalf("adopted source: got %q, want manual", got.Source)
	}

	// 伪造 auto → 400，状态不变。
	rec = doReq(e, http.MethodPatch, "/api/v1/services/"+strconv.Itoa(svc.ID), `{"source":"auto"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("set source=auto: status = %d, want 400", rec.Code)
	}
	got2, _ := c.Service.Get(ctx, svc.ID)
	if got2.Source != entservice.SourceManual {
		t.Fatalf("state must be unchanged after rejected auto, got %q", got2.Source)
	}
}
