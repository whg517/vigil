//go:build integration

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestHealth_AllUp 依赖（PG+Redis）都通时 /health 返回 200 且 checks 标记 up。
func TestHealth_AllUp(t *testing.T) {
	env := Setup(t)
	t.Cleanup(func() { env.ResetDB(t) })

	resp, err := http.Get(env.BaseURL() + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health: got %d, want 200", resp.StatusCode)
	}
}

// TestHealth_VersionExposed /health 暴露 version 字段（供 K8s/运维确认版本）。
func TestHealth_VersionExposed(t *testing.T) {
	env := Setup(t)
	t.Cleanup(func() { env.ResetDB(t) })

	resp, err := http.Get(env.BaseURL() + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got struct {
		Status  string            `json:"status"`
		Version string            `json:"version"`
		Checks  map[string]string `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode /health: %v", err)
	}
	if got.Version == "" {
		t.Errorf("/health: version empty, want non-empty")
	}
	if got.Checks["postgres"] != "up" {
		t.Errorf("/health: postgres check = %q, want up", got.Checks["postgres"])
	}
	if got.Checks["redis"] != "up" {
		t.Errorf("/health: redis check = %q, want up", got.Checks["redis"])
	}
}
