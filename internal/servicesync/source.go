// Package servicesync 实现能力域 4 方案C P2：主动从外部源同步 Service。
//
// 周期性拉取「期望的服务清单」，upsert source=auto 的服务（挂解析出的团队、继承团队默认
// 升级策略），绝不触碰 source=manual。对应 docs/capabilities/02-triage-routing.md §3.5。
// 与懒供给（P1，triage 侧未路由即时建服务）互补：这里是「服务上线即存在」的主动路径。
package servicesync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// DesiredService 外部源声明的期望服务（清单一条）。
// team 为归属团队 slug（空则用配置的兜底团队）；labels 供路由匹配（缺省时告警仍可用
// labels["service"]==slug 的 slug 直达命中该服务，见 triage.route）。
type DesiredService struct {
	Slug   string            `json:"slug"`
	Name   string            `json:"name"`
	Team   string            `json:"team"`
	Labels map[string]string `json:"labels"`
}

// Source 服务清单源。List 返回一份期望的服务全集（幂等，多次调用应反映源当前状态）。
type Source interface {
	List(ctx context.Context) ([]DesiredService, error)
}

// FileSource 从本地 JSON 文件读取服务清单（GitOps：清单随仓库/挂载卷更新即被下轮同步拾取）。
type FileSource struct{ Path string }

// List 读取并解析文件。
func (s FileSource) List(_ context.Context) ([]DesiredService, error) {
	b, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, fmt.Errorf("read service catalog file %q: %w", s.Path, err)
	}
	return parseCatalog(b)
}

// HTTPSource 从 HTTP 端点拉取服务清单 JSON（对接服务目录/CMDB 导出）。
type HTTPSource struct {
	URL    string
	Client *http.Client
}

// List GET URL 并解析响应体。
func (s HTTPSource) List(ctx context.Context) ([]DesiredService, error) {
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("build service catalog request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch service catalog %q: %w", s.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("service catalog %q: status %d", s.URL, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read service catalog body: %w", err)
	}
	return parseCatalog(b)
}

// parseCatalog 解析 JSON 数组为清单。
func parseCatalog(b []byte) ([]DesiredService, error) {
	var list []DesiredService
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("parse service catalog json: %w", err)
	}
	return list, nil
}
