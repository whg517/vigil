package logger

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

func TestNew_Production(t *testing.T) {
	l, err := New("production", "info")
	if err != nil {
		t.Fatalf("New production: %v", err)
	}
	if l == nil {
		t.Fatal("production logger nil")
	}
	// 能正常记日志不 panic
	l.Info("test")
}

func TestNew_Development(t *testing.T) {
	l, err := New("development", "debug")
	if err != nil {
		t.Fatalf("New development: %v", err)
	}
	if l == nil {
		t.Fatal("development logger nil")
	}
	l.Debug("test debug")
}

func TestNew_InvalidLevel(t *testing.T) {
	_, err := New("production", "bogus")
	if err == nil {
		t.Error("invalid level should return error")
	}
}

func TestParseLevel(t *testing.T) {
	// 有效级别
	for _, lvl := range []string{"debug", "info", "warn", "error"} {
		if _, err := parseLevel(lvl); err != nil {
			t.Errorf("parseLevel(%q): unexpected err %v", lvl, err)
		}
	}
	// 无效级别应报错
	if _, err := parseLevel("bogus"); err == nil {
		t.Error("parseLevel(bogus) expected error, got nil")
	}
}

func TestInto_From(t *testing.T) {
	l, _ := New("development", "info")
	ctx := context.Background()

	// 无 logger 时返回 Nop（不 panic）
	fromEmpty := From(ctx)
	if fromEmpty == nil {
		t.Fatal("From empty ctx returned nil")
	}
	// Nop logger 能记日志不 panic
	fromEmpty.Info("nop")

	// 注入后能取出
	ctxWith := Into(ctx, l)
	fromCtx := From(ctxWith)
	if fromCtx != l {
		t.Error("From did not return injected logger")
	}
}

func TestFrom_NilLoggerInCtx(t *testing.T) {
	// 注入 nil 不应 panic，From 返回 Nop
	ctx := context.WithValue(context.Background(), ctxKey{}, (*zap.Logger)(nil))
	got := From(ctx)
	if got == nil {
		t.Fatal("From with nil logger returned nil")
	}
	got.Info("should be nop")
}
