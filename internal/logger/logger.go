// Package logger 提供全局结构化日志（基于 zap）。
//
// 对应 docs/architecture.md §7.3 可观测性：结构化日志，可对接日志系统。
// 业务代码用 logger.From(ctx) 获取带请求上下文的 logger。
package logger

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type ctxKey struct{}

// New 根据环境创建 logger。
// production 用 JSON 编码 + info 级；development 用 console 编码 + debug 级。
func New(env, level string) (*zap.Logger, error) {
	var cfg zap.Config
	if env == "production" {
		cfg = zap.NewProductionConfig()
	} else {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder // 开发态彩色级别
	}

	// 覆盖日志级别
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}
	cfg.Level = zap.NewAtomicLevelAt(lvl)

	l, err := cfg.Build(zap.AddCallerSkip(1))
	if err != nil {
		return nil, fmt.Errorf("build logger: %w", err)
	}
	return l, nil
}

func parseLevel(s string) (zapcore.Level, error) {
	var lvl zapcore.Level
	if err := lvl.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("invalid log level %q: %w", s, err)
	}
	return lvl, nil
}

// Into 把 logger 注入 context，供下游通过 From(ctx) 取出。
// 注：当前全项目通过 main 的全局 *zap.Logger 直接使用，未走 context 注入。
// 如未来需要请求级日志关联（trace_id 等），可启用此机制。
func Into(ctx context.Context, l *zap.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From 从 context 取出 logger；若无则返回 Nop logger（不报错）。
func From(ctx context.Context) *zap.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*zap.Logger); ok && l != nil {
		return l
	}
	return zap.NewNop()
}
