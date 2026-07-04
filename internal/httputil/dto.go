// Package httputil 提供跨域共享的 HTTP 响应 DTO。
//
// 这些结构体存在的唯一目的是让 swag 生成的 OpenAPI spec 有真实 schema，
// 替换 handler 里裸 map[string]any 的返回（裸 map 在 spec 里只能渲染成空 object）。
// 不含业务逻辑，仅是序列化契约。
package httputil

// ErrorResponse 统一错误响应体。
// 各 handler 错误分支统一用 c.JSON(status, httputil.ErrorResponse{Error: ...}），
// 让 @Failure 注解能引用具名 schema。
type ErrorResponse struct {
	Error string `json:"error" example:"invalid id"`
	// Code 机器可读错误码（可选，非 4xx 通用错误可留空）。
	Code string `json:"code,omitempty" example:"invalid_argument"`
	// Details 结构化补充信息（可选，如字段级校验错误）。
	Details any `json:"details,omitempty"`
}

// Paginated 分页列表响应包装（swag v2 rc5 泛型实例化）。
// 用法（注解）：@Success 200 {object} httputil.Paginated[ent.Incident]
// handler 返回：c.JSON(200, httputil.Paginated[*ent.Incident]{Items: ..., Total: ..., Limit: ..., Offset: ...})
type Paginated[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// AckResponse 通用同步确认响应（接入 webhook、IM 回调等异步流水线入口）。
// status 取值见各 handler：ingestion 为 accepted/rate_limited/backpressure；
// IM 回调为 ok/ignored/sent。
type AckResponse struct {
	Status string `json:"status" example:"accepted"`
	// RawEventID ingestion 落库的 RawEvent ID（限接入 webhook）。
	RawEventID int `json:"raw_event_id,omitempty" example:"42"`
	// ID 通用资源 ID（如出站 webhook 投递重放的 delivery id）。
	ID int `json:"id,omitempty" example:"42"`
	// RetryAfter 限流/背压时建议重试秒数（限接入 webhook）。
	RetryAfter int `json:"retry_after,omitempty" example:"60"`
}
