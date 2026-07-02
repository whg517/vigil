// Package errs 定义统一错误模型（BE-01/BE-03）。
//
// 目标：消除 handler 把底层 err.Error() 直泄给前端的反模式。
// 业务 handler 不再写 c.JSON(500, ErrorResponse{Error: err.Error()})，
// 改用 errs.BadRequest(c, "invalid id") / errs.Internal(c, log, err) 等 helper：
//   - 4xx 类错误：返回稳定的用户可读 message（不含内部细节）+ 可选 code
//   - 5xx 类错误：返回通用 "internal error" 给前端，真实 err 用 log.Error + RequestID 记录
//
// 错误码规范（machine-readable，供前端按 code 做差异化提示）：
//   - invalid_argument      参数校验失败（400）
//   - not_found             资源不存在（404）
//   - unauthenticated       未认证（401）
//   - permission_denied     无权限（403）
//   - already_exists        资源已存在（409）
//   - rate_limited          限流（429）
//   - failed_precondition   前置条件不满足（如状态机非法流转，400）
//   - internal              内部错误（500，不泄细节）
//
// 与 httputil.ErrorResponse 协同：helper 直接写 HTTP 响应，避免每个 handler 重复。
package errs

import (
	"net/http"

	"github.com/kevin/vigil/internal/httputil"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

// 稳定的机器可读错误码（BE-01）。
const (
	CodeInvalidArgument    = "invalid_argument"
	CodeNotFound           = "not_found"
	CodeUnauthenticated    = "unauthenticated"
	CodePermissionDenied   = "permission_denied"
	CodeAlreadyExists      = "already_exists"
	CodeRateLimited        = "rate_limited"
	CodeFailedPrecondition = "failed_precondition"
	CodeInternal           = "internal"
)

// 面向前端的通用安全 message（5xx 类不泄内部细节）。
const (
	msgInternal = "internal error"
)

// globalLogger 全局 logger（FIX-3：消除 errs.Internal(c, nil, err) 的可观测性回归）。
// 由 SetLogger 在装配期注入（wire 调一次），Internal/FailNotFound 在调用方传 nil 时回退用它。
// logger 是启动期设置一次的单例，全局状态在此处可接受（与 echo.New 同为装配对象）。
var globalLogger *zap.Logger

// SetLogger 设置全局 logger（装配期由 server.Wire 调用一次）。
// 设置后，所有 errs.Internal(c, nil, err) 会用此 logger 记录详细 err，
// 无需每个 handler 持有 logger——消除 BE-01 引入的"传 nil 导致不记录"回归。
func SetLogger(log *zap.Logger) { globalLogger = log }

// BadRequest 400 invalid_argument。msg 为用户可读提示（非底层 err）。
func BadRequest(c *echo.Context, msg string) error {
	return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: msg, Code: CodeInvalidArgument})
}

// BadRequestWith 400 自定义 code（如 failed_precondition）。
func BadRequestWith(c *echo.Context, code, msg string) error {
	return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: msg, Code: code})
}

// NotFound 404 not_found。msg 默认 "not found"。
func NotFound(c *echo.Context, msg ...string) error {
	m := "not found"
	if len(msg) > 0 {
		m = msg[0]
	}
	return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: m, Code: CodeNotFound})
}

// Unauthorized 401 unauthenticated。
func Unauthorized(c *echo.Context, msg string) error {
	if msg == "" {
		msg = "unauthenticated"
	}
	return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: msg, Code: CodeUnauthenticated})
}

// Forbidden 403 permission_denied。
func Forbidden(c *echo.Context, msg string) error {
	if msg == "" {
		msg = "forbidden"
	}
	return c.JSON(http.StatusForbidden, httputil.ErrorResponse{Error: msg, Code: CodePermissionDenied})
}

// Conflict 409 already_exists。
func Conflict(c *echo.Context, msg string) error {
	return c.JSON(http.StatusConflict, httputil.ErrorResponse{Error: msg, Code: CodeAlreadyExists})
}

// RateLimited 429 rate_limited。retryAfterSec 可选（建议重试秒数，前端可读）。
func RateLimited(c *echo.Context, msg string, retryAfterSec int) error {
	return c.JSON(http.StatusTooManyRequests, httputil.ErrorResponse{
		Error: msg, Code: CodeRateLimited,
		Details: map[string]any{"retry_after_seconds": retryAfterSec},
	})
}

// Internal 500 internal。真实 err 用 log 记录（含 RequestID 串联排障），
// 前端只收到通用 "internal error"，杜绝底层细节（SQL 错误/路径/表名）泄露。
//
// log 解析顺序：显式传入 > 全局（SetLogger 注入）> 不记录（纯测试）。
// 生产装配（server.Wire 调 errs.SetLogger）后，即使 handler 传 nil 也会记录，
// 消除 BE-01 引入的"传 nil 导致生产不记录 err"回归（FIX-3）。
func Internal(c *echo.Context, log *zap.Logger, err error, msg ...string) error {
	if log == nil {
		log = globalLogger // 回退全局（装配期注入）
	}
	if log != nil && err != nil {
		// RequestID 由 echo middleware 注入到 response header，日志此处带 err 即可串联。
		log.Error("request internal error",
			zap.Error(err),
			zap.String("path", c.Path()),
			zap.String("method", c.Request().Method),
		)
	}
	m := msgInternal
	if len(msg) > 0 && msg[0] != "" {
		m = msg[0]
	}
	return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: m, Code: CodeInternal})
}

// FailNotFound 把 ent NotFound 错误转为 404，其余 err 走 Internal。
// 便于 incident/runbook 等按 id 查询的 handler 收口错误处理。
//
// 用法：
//
//	obj, err := h.db.Incident.Get(ctx, id)
//	if err != nil {
//	    return errs.FailNotFound(c, log, err, "incident")
//	}
func FailNotFound(c *echo.Context, log *zap.Logger, err error, resource string) error {
	if isNotFound(err) {
		return NotFound(c, resource+" not found")
	}
	return Internal(c, log, err)
}

// isNotFound 判断是否"未找到"类错误（ent NotFound / sql no rows）。
// 不导入 ent 包（避免 errs → ent 反向依赖），改用字符串匹配，
// 覆盖 ent.NotFound 与 database/sql.ErrNoRows 两类。
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// ent: "ent: ... not found"; sql: "sql: no rows in result set"
	for _, marker := range []string{"not found", "no rows"} {
		if contains(msg, marker) {
			return true
		}
	}
	return false
}

// contains 简化 strings.Contains（避免增加 import，本文件已尽量精简）。
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
