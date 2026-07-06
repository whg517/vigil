// callback_handler.go 工单侧状态回调端点（N1.3 工单双向回写）。
//
// 对应 docs/capabilities/10-integrations-analytics.md §A2「工单系统」双向打通：
// T4.3 只做了 Vigil→工单单向（建单 + ActionItem done 单向推送），工单侧关闭/推进不回写
// Vigil。本端点补上反向：外部工单系统在工单状态变更（关闭/进行中/重开）时回调 Vigil，
// 据 external_id / tracker_url 匹配对应 ActionItem 并更新其 status（open→in_progress→done）。
//
// 安全（与 T4.3 凭据面对称）：
//   - 回调按 TicketIntegration.callback_secret 做 HMAC-SHA256 验签（复用出站 webhook 同款
//     算法 webhook.Sign），防伪造/防重放（带时间戳）。密钥经 T6.3 cipher 解密后比对。
//   - 未配 callback_secret 的集成不接受回调（拒绝）——不给「无密钥即放行」的后门。
//   - 路径带集成 id（/webhooks/ticket/:id）：只用该集成的密钥验签，避免跨集成串签。
//
// best-effort / 幂等：匹配不到 ActionItem → 200 ignored（回调常与本系统无关，忽略是常态）；
// 目标已是回调状态 → 200 unchanged（重复回调不重复变更）。落审计（谁/哪条工单改了哪个改进项）。
package ticket

import (
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/internal/auth"
	"github.com/kevin/vigil/internal/crypto"
	"github.com/kevin/vigil/internal/httputil"
	"github.com/kevin/vigil/internal/webhook"

	"github.com/labstack/echo/v5"
)

// callbackHeaderSignature 回调验签头：hex(HMAC-SHA256(callback_secret, timestamp + "." + body))。
// 与出站 webhook 同名头 + 同算法（webhook.Sign），对接方一套实现两处复用。
const callbackHeaderSignature = webhook.HeaderSignature

// callbackHeaderTimestamp 回调时间戳头（Unix 秒），防重放（超窗拒绝）。
const callbackHeaderTimestamp = "X-Vigil-Timestamp"

// callbackMaxSkew 回调时间戳允许的最大偏移（防重放窗口）。超此偏移的回调拒绝。
const callbackMaxSkew = 5 * time.Minute

// callbackMaxBody 回调 body 上限（防超大 payload）。工单状态回调 payload 很小，1MB 足够。
const callbackMaxBody = 1 << 20

// CallbackHandler 工单侧状态回调端点。独立于配置 handler：这是公开入向端点
// （外部工单系统调用，不走 JWT/RBAC），鉴权靠 per-integration HMAC 验签。
type CallbackHandler struct {
	db     *ent.Client
	engine *Engine
	cipher *crypto.Cipher      // 解密 callback_secret（T6.3，可选；nil 时按明文比对，向后兼容）
	audit  *auth.AuditRecorder // 回调改状态留痕（可选注入）
}

// NewCallbackHandler 创建回调 handler。engine 承载匹配 + 状态落库逻辑。
func NewCallbackHandler(db *ent.Client, engine *Engine) *CallbackHandler {
	return &CallbackHandler{db: db, engine: engine}
}

// SetCipher 注入凭据加密器（T6.3）：callback_secret 与 credential 同为密文存储，
// 验签前用同一 cipher 解密。nil 或解密失败时按明文比对（向后兼容既有明文密钥）。
func (h *CallbackHandler) SetCipher(c *crypto.Cipher) { h.cipher = c }

// SetAuditRecorder 注入审计记录器：回调真正改动 ActionItem 状态时留痕。
func (h *CallbackHandler) SetAuditRecorder(r *auth.AuditRecorder) { h.audit = r }

// Register 挂载公开回调路由（不走 RBAC，验签在 handler 内完成）。
func (h *CallbackHandler) Register(g *echo.Group) {
	g.POST("/webhooks/ticket/:id", h.callback)
}

// callbackReq 工单侧回调 body（对接方按此结构 POST）。
type callbackReq struct {
	// ExternalID 外部工单 id（与建单时回写的 ActionItem.external_id 对应，匹配主键）。
	ExternalID string `json:"external_id"`
	// TrackerURL 工单 URL（兜底匹配，external_id 缺失/未回写时用）。
	TrackerURL string `json:"tracker_url"`
	// Status 外部工单当前状态（closed/resolved/in_progress/open/...，归一到 ActionItem 三态）。
	Status string `json:"status"`
}

// callback 处理工单侧状态回调。
//
// @Summary      Ticket status callback
// @Description  外部工单系统状态变更回调；HMAC 验签后据 external_id/tracker_url 匹配 ActionItem 更新状态（N1.3）。
// @Tags         ticket-integration
// @Accept       json
// @Produce      json
// @Param        id             path    int     true   "工单集成 ID"
// @Param        X-Vigil-Signature  header  string  true   "hex(HMAC-SHA256(callback_secret, timestamp + \".\" + body))"
// @Param        X-Vigil-Timestamp  header  string  true   "Unix 秒时间戳（防重放）"
// @Success      200  {object}  map[string]any
// @Failure      400  {object}  httputil.ErrorResponse
// @Failure      401  {object}  httputil.ErrorResponse
// @Failure      404  {object}  httputil.ErrorResponse
// @Router       /webhooks/ticket/{id} [post]
func (h *CallbackHandler) callback(c *echo.Context) error {
	integID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid id"})
	}
	ctx := c.Request().Context()

	// 读 body（限长，防超大 payload）——验签对 body 原文签，须先读出原始字节。
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, callbackMaxBody))
	if err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "read body failed"})
	}

	// 取集成 + 其 callback_secret（Sensitive，须直接查库拿到内存态明文/密文）。
	integ, err := h.db.TicketIntegration.Get(ctx, integID)
	if err != nil {
		// 不存在的集成：404（不泄露是否配了密钥，仅告知路径无效）。
		return c.JSON(http.StatusNotFound, httputil.ErrorResponse{Error: "ticket integration not found"})
	}
	secret := h.resolveCallbackSecret(integ.CallbackSecret)
	if secret == "" {
		// 未配回调密钥：该集成不接受回调（不给无密钥后门）。401 而非 404，明确是鉴权拒绝。
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "callback not enabled for this integration"})
	}
	if !integ.Enabled {
		// 已禁用集成不接受回调（与建单/同步一致：disabled 即停用）。
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "integration disabled"})
	}

	// 验签（HMAC + 时间戳防重放）。任一不过 → 401，不泄露具体原因（防探测）。
	if !verifyCallbackSignature(secret, body,
		c.Request().Header.Get(callbackHeaderTimestamp),
		c.Request().Header.Get(callbackHeaderSignature)) {
		return c.JSON(http.StatusUnauthorized, httputil.ErrorResponse{Error: "invalid signature"})
	}

	var req callbackReq
	if err := json.Unmarshal(body, &req); err != nil {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid body"})
	}
	if req.ExternalID == "" && req.TrackerURL == "" {
		return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "external_id or tracker_url required"})
	}

	res, err := h.engine.HandleCallback(ctx, req.ExternalID, req.TrackerURL, req.Status)
	if err != nil {
		if errors.Is(err, ErrCallbackUnknownStatus) {
			return c.JSON(http.StatusBadRequest, httputil.ErrorResponse{Error: "unrecognized status"})
		}
		return c.JSON(http.StatusInternalServerError, httputil.ErrorResponse{Error: "callback processing failed"})
	}
	if !res.Matched {
		// 未匹配到 ActionItem：忽略（回调常与本系统无关，非错误）。200 便于回调方视为已收妥。
		return c.JSON(http.StatusOK, map[string]any{"status": "ignored", "reason": "no matching action item"})
	}
	if res.Changed {
		h.auditCallback(c, integ, res)
		return c.JSON(http.StatusOK, map[string]any{
			"status": "updated", "action_item_id": res.ActionItemID,
			"from": res.FromStatus, "to": res.ToStatus,
		})
	}
	// 幂等：已是目标状态，不重复变更、不重复留痕。
	return c.JSON(http.StatusOK, map[string]any{
		"status": "unchanged", "action_item_id": res.ActionItemID,
	})
}

// resolveCallbackSecret 解出 callback_secret 明文（供验签比对）。
// cipher 非 nil 且能解密 → 明文；否则（无 cipher / 解密失败）按明文透传（向后兼容既有明文密钥）。
// 与 engine.resolveCredential 同款逻辑（统一加密机制，T6.3）。
func (h *CallbackHandler) resolveCallbackSecret(stored string) string {
	if stored == "" || h.cipher == nil {
		return stored
	}
	if plain, err := h.cipher.Decrypt(stored); err == nil {
		return plain
	}
	return stored
}

// auditCallback 回调改动 ActionItem 状态时留痕（谁/哪条工单改了哪个改进项）。
// 回调无登录用户（外部系统调用），actor=0；ResourceName 记来源集成名 + 外部工单迁移。
func (h *CallbackHandler) auditCallback(c *echo.Context, integ *ent.TicketIntegration, res *CallbackResult) {
	if h.audit == nil {
		return
	}
	e := auth.AuditEntryFromRequest(c.Request(), 0, integ.Name)
	e.Action = auth.ActionTicketCallbackSync
	e.ResourceType = "action_item"
	e.ResourceID = res.ActionItemID
	e.ResourceName = integ.Name
	e.Detail = map[string]any{
		"integration_id": integ.ID,
		"from":           res.FromStatus,
		"to":             res.ToStatus,
	}
	h.audit.MustRecord(c.Request().Context(), e)
}

// verifyCallbackSignature 验证回调签名（HMAC-SHA256 + 时间戳防重放）。
// 复用出站 webhook 的 Sign 算法（对拍口径一致）。空密钥/空签名/超窗/不匹配均返回 false。
func verifyCallbackSignature(secret string, body []byte, tsHeader, sigHeader string) bool {
	if secret == "" || sigHeader == "" || tsHeader == "" {
		return false
	}
	tsUnix, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return false
	}
	ts := time.Unix(tsUnix, 0)
	// 防重放：时间戳偏移超窗（过去或未来）拒绝。
	if skew := time.Since(ts); skew > callbackMaxSkew || skew < -callbackMaxSkew {
		return false
	}
	_, expected := webhook.Sign(secret, body, ts)
	// 常量时间比较，防时序侧信道。
	return hmac.Equal([]byte(expected), []byte(sigHeader))
}
