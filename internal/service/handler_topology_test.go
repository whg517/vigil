// handler_topology_test.go 服务拓扑 depends_on 契约测试（T6.2 / M4.4 服务拓扑）。
//
// 覆盖：create 收 depends_on_ids 并落库；update 全量替换语义；自引用被剔除；
// GET /services/:id/dependencies 返回一层依赖（depends_on 下游 + dependents 上游=影响面）。
// 无 authz 注入（checkAccess 降级放行），聚焦请求/响应契约。
package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/enttest"

	"github.com/labstack/echo/v5"
	_ "github.com/mattn/go-sqlite3"
)

// topoSetup 建 3 个服务：web、db、cache，返回 client/echo + 各 id。
func topoSetup(t *testing.T) (*ent.Client, *echo.Echo, int, int, int) {
	t.Helper()
	c := enttest.Open(t, "sqlite3", "file:svc_topo_"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = c.Close() })
	ctx := t.Context()
	web := c.Service.Create().SetName("web").SetSlug("web").SaveX(ctx)
	db := c.Service.Create().SetName("db").SetSlug("db").SaveX(ctx)
	cache := c.Service.Create().SetName("cache").SetSlug("cache").SaveX(ctx)

	h := NewHandler(c)
	e := echo.New()
	h.Register(e.Group("/api/v1"))
	return c, e, web.ID, db.ID, cache.ID
}

func decodeDependsOn(t *testing.T, rec *httptest.ResponseRecorder) map[int]bool {
	t.Helper()
	var resp struct {
		DependsOnIDs []int `json:"depends_on_ids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode resp: %v (body=%s)", err, rec.Body.String())
	}
	return toSet(resp.DependsOnIDs)
}

// TestCreateWithDependsOn 创建时收 depends_on_ids 并回带。
func TestCreateWithDependsOn(t *testing.T) {
	_, e, _, dbID, cacheID := topoSetup(t)
	body := `{"name":"api","slug":"api","depends_on_ids":[` +
		strconv.Itoa(dbID) + `,` + strconv.Itoa(cacheID) + `]}`
	rec := doJSON(e, http.MethodPost, "/api/v1/services", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	deps := decodeDependsOn(t, rec)
	if !deps[dbID] || !deps[cacheID] || len(deps) != 2 {
		t.Errorf("depends_on_ids: got %v, want {%d,%d}", deps, dbID, cacheID)
	}
}

// TestUpdateReplacesDependsOn update 全量替换 depends_on。
func TestUpdateReplacesDependsOn(t *testing.T) {
	c, e, webID, dbID, cacheID := topoSetup(t)
	// web 初始依赖 db。
	c.Service.UpdateOneID(webID).AddDependsOnIDs(dbID).ExecX(t.Context())
	// 替换为 [cache]。
	body := `{"depends_on_ids":[` + strconv.Itoa(cacheID) + `]}`
	rec := doJSON(e, http.MethodPatch, "/api/v1/services/"+strconv.Itoa(webID), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	deps := decodeDependsOn(t, rec)
	if !deps[cacheID] || deps[dbID] || len(deps) != 1 {
		t.Errorf("depends_on after replace: got %v, want {%d}", deps, cacheID)
	}
}

// TestSelfDependencyFiltered 服务不能依赖自己（自引用被剔除）。
func TestSelfDependencyFiltered(t *testing.T) {
	_, e, webID, dbID, _ := topoSetup(t)
	// web 依赖 [web(自己), db] → self 应被剔除，只剩 db。
	body := `{"depends_on_ids":[` + strconv.Itoa(webID) + `,` + strconv.Itoa(dbID) + `]}`
	rec := doJSON(e, http.MethodPatch, "/api/v1/services/"+strconv.Itoa(webID), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	deps := decodeDependsOn(t, rec)
	if deps[webID] {
		t.Errorf("self dependency must be filtered, got %v", deps)
	}
	if !deps[dbID] || len(deps) != 1 {
		t.Errorf("depends_on: got %v, want {%d}", deps, dbID)
	}
}

// TestDependenciesEndpoint GET /services/:id/dependencies 返回一层依赖 + 影响面（dependents）。
func TestDependenciesEndpoint(t *testing.T) {
	c, e, webID, dbID, cacheID := topoSetup(t)
	// 拓扑：web → db，cache → db。故 db 的 dependents = {web, cache}（db 故障影响面）。
	c.Service.UpdateOneID(webID).AddDependsOnIDs(dbID).ExecX(t.Context())
	c.Service.UpdateOneID(cacheID).AddDependsOnIDs(dbID).ExecX(t.Context())

	// 查 db 的依赖拓扑：depends_on 为空，dependents = {web, cache}。
	rec := doJSON(e, http.MethodGet, "/api/v1/services/"+strconv.Itoa(dbID)+"/dependencies", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("dependencies: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		ServiceID int `json:"service_id"`
		DependsOn []struct {
			ID int `json:"id"`
		} `json:"depends_on"`
		Dependents []struct {
			ID int `json:"id"`
		} `json:"dependents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if resp.ServiceID != dbID {
		t.Errorf("service_id: got %d, want %d", resp.ServiceID, dbID)
	}
	if len(resp.DependsOn) != 0 {
		t.Errorf("db depends_on should be empty, got %+v", resp.DependsOn)
	}
	dependents := map[int]bool{}
	for _, d := range resp.Dependents {
		dependents[d.ID] = true
	}
	if !dependents[webID] || !dependents[cacheID] || len(dependents) != 2 {
		t.Errorf("db dependents (影响面): got %v, want {%d,%d}", dependents, webID, cacheID)
	}

	// 查 web 的依赖拓扑：depends_on = {db}，dependents 为空。
	rec2 := doJSON(e, http.MethodGet, "/api/v1/services/"+strconv.Itoa(webID)+"/dependencies", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("dependencies web: got %d, want 200", rec2.Code)
	}
	var resp2 struct {
		DependsOn []struct {
			ID int `json:"id"`
		} `json:"depends_on"`
		Dependents []struct {
			ID int `json:"id"`
		} `json:"dependents"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if len(resp2.DependsOn) != 1 || resp2.DependsOn[0].ID != dbID {
		t.Errorf("web depends_on: got %+v, want {%d}", resp2.DependsOn, dbID)
	}
	if len(resp2.Dependents) != 0 {
		t.Errorf("web dependents should be empty, got %+v", resp2.Dependents)
	}
}

// TestDependenciesNotFound 不存在的服务返回 404。
func TestDependenciesNotFound(t *testing.T) {
	_, e, _, _, _ := topoSetup(t)
	rec := doJSON(e, http.MethodGet, "/api/v1/services/99999/dependencies", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("dependencies unknown: got %d, want 404", rec.Code)
	}
}
