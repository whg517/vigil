package config

import (
	"testing"
)

// TestLoad_Defaults 验证默认值在无环境变量时正确填充。
func TestLoad_Defaults(t *testing.T) {
	t.Setenv("VIGIL_ENV", "") // 清空环境，走默认

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// 检查关键默认值（与 config.go 中的 default tag 一致）
	cases := []struct{ name, got, want string }{
		{"env", cfg.App.Env, "development"},
		{"log_level", cfg.App.LogLevel, "info"},
		{"http_addr", cfg.HTTP.Addr, ":8080"},
		{"db_host", cfg.DB.Host, "localhost"},
		{"db_name", cfg.DB.Name, "vigil"},
		{"db_sslmode", cfg.DB.SSLMode, "disable"},
		{"redis_addr", cfg.Redis.Addr, "localhost:6379"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}

	// DB 端口默认 5432
	if cfg.DB.Port != 5432 {
		t.Errorf("db_port: got %d, want 5432", cfg.DB.Port)
	}
	// Asynq 并发默认 10
	if cfg.Asynq.Concurrency != 10 {
		t.Errorf("asynq_concurrency: got %d, want 10", cfg.Asynq.Concurrency)
	}
}

// TestLoad_EnvOverride 验证环境变量覆盖默认值。
func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("VIGIL_HTTP_ADDR", ":9090")
	t.Setenv("VIGIL_DB_HOST", "db.internal")
	t.Setenv("VIGIL_DB_PORT", "6543")
	t.Setenv("VIGIL_APP_ENV", "production")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.HTTP.Addr != ":9090" {
		t.Errorf("http_addr override: got %q, want :9090", cfg.HTTP.Addr)
	}
	if cfg.DB.Host != "db.internal" {
		t.Errorf("db_host override: got %q", cfg.DB.Host)
	}
	if cfg.DB.Port != 6543 {
		t.Errorf("db_port override: got %d, want 6543", cfg.DB.Port)
	}
	if !cfg.App.IsProduction() {
		t.Error("IsProduction should be true for env=production")
	}
}

// TestDB_DSN 验证 DSN 拼接格式。
func TestDB_DSN(t *testing.T) {
	d := DB{Host: "h", Port: 5432, User: "u", Password: "p", Name: "n", SSLMode: "disable"}
	got := d.DSN()
	want := "host=h port=5432 user=u password=p dbname=n sslmode=disable"
	if got != want {
		t.Errorf("DSN: got %q, want %q", got, want)
	}
}
