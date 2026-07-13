// smtp_server.go SMTP 入向接收端(ADR-0038):遗留监控系统的邮件告警 → Event。
//
// 鉴权 = 收件人即令牌:收件地址 local part 必须等于某个 type=email 且 enabled 的
// Integration 的 token,查不到在 RCPT 阶段即拒(550),不进入 DATA。
// 收信后复用 IngestRaw 核心,与 webhook 入口同享「先落库不丢/限流/背压/回灌」契约。
//
// 部署边界:默认关闭(VIGIL_SMTP_IN_ENABLED);开启后端口仅应内网可达,
// 不做 STARTTLS/SMTP AUTH——公网暴露属错误部署(operations checklist 已注明)。
package ingestion

import (
	"context"
	"crypto/sha1" // #nosec G505 -- 仅用于生成去重指纹,非密码学用途(与 DedupKey 同源做法)
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/kevin/vigil/ent"
	"github.com/kevin/vigil/ent/integration"
)

// SMTPServer SMTP 入向接收端。Start 后台监听,Shutdown 优雅退出。
type SMTPServer struct {
	db      *ent.Client
	handler *Handler // 复用 IngestRaw
	srv     *smtp.Server
}

// NewSMTPServer 创建接收端。addr 形如 ":2525"。
func NewSMTPServer(db *ent.Client, h *Handler, addr string) *SMTPServer {
	s := &SMTPServer{db: db, handler: h}
	srv := smtp.NewServer(&smtpBackend{s: s})
	srv.Addr = addr
	srv.Domain = "vigil"
	srv.ReadTimeout = 30 * time.Second
	srv.WriteTimeout = 30 * time.Second
	srv.MaxMessageBytes = maxPayloadBytes // 与 webhook 同一 payload 上限
	srv.MaxRecipients = 8
	s.srv = srv
	return s
}

// Start 后台启动监听。监听失败只记日志(告警邮件送不进来须可见,但不拖垮主进程)。
func (s *SMTPServer) Start() {
	go func() {
		slog.Info("smtp inbound listening", "addr", s.srv.Addr)
		if err := s.srv.ListenAndServe(); err != nil {
			slog.Error("smtp inbound server exited", "error", err)
		}
	}()
}

// Shutdown 优雅关闭。
func (s *SMTPServer) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

type smtpBackend struct{ s *SMTPServer }

func (b *smtpBackend) NewSession(_ *smtp.Conn) (smtp.Session, error) {
	return &smtpSession{s: b.s}, nil
}

// smtpSession 单连接会话:RCPT 阶段做 token→Integration 校验,DATA 阶段落库入队。
type smtpSession struct {
	s      *SMTPServer
	from   string
	integs []*ent.Integration // RCPT 校验通过的接入点(去重后逐一投递)
}

func (se *smtpSession) Mail(from string, _ *smtp.MailOptions) error {
	se.from = from
	return nil
}

func (se *smtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	// local part 即 Integration token(ADR-0038 收件人即令牌)
	local := to
	if i := strings.IndexByte(to, '@'); i > 0 {
		local = to[:i]
	}
	integ, err := se.s.db.Integration.Query().
		Where(integration.TokenEQ(local), integration.TypeEQ("email"), integration.EnabledEQ(true)).
		Only(context.Background())
	if err != nil {
		// 与 webhook 401 同语义:不区分"不存在/未启用",拒收不落库(防探测)
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "unknown recipient"}
	}
	for _, exist := range se.integs {
		if exist.ID == integ.ID {
			return nil
		}
	}
	se.integs = append(se.integs, integ)
	return nil
}

func (se *smtpSession) Data(r io.Reader) error {
	if len(se.integs) == 0 {
		return &smtp.SMTPError{Code: 554, Message: "no valid recipient"}
	}
	msg, err := mail.ReadMessage(io.LimitReader(r, int64(maxPayloadBytes)))
	if err != nil {
		return &smtp.SMTPError{Code: 554, Message: "malformed message"}
	}
	body, _ := io.ReadAll(io.LimitReader(msg.Body, int64(maxPayloadBytes)))

	// 以 JSON 信封落 RawEvent.payload:归一化(EmailAdapter)与人工排查都读它。
	envelope, _ := json.Marshal(emailEnvelope{
		From:      se.from,
		Subject:   decodeMIMEHeader(msg.Header.Get("Subject")),
		MessageID: strings.Trim(msg.Header.Get("Message-ID"), "<>"),
		Date:      msg.Header.Get("Date"),
		Body:      string(body),
	})
	headers := map[string]string{"Transport": "smtp", "From": se.from}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, integ := range se.integs {
		ack, _ := se.s.handler.IngestRaw(ctx, integ, envelope, headers)
		slog.Info("smtp inbound accepted", "integration", integ.ID, "status", ack.Status, "raw_event", ack.RawEventID)
	}
	return nil
}

func (se *smtpSession) Reset()        { se.integs = nil; se.from = "" }
func (se *smtpSession) Logout() error { return nil }

// emailEnvelope RawEvent.payload 的邮件信封结构(EmailAdapter 消费)。
type emailEnvelope struct {
	From      string `json:"from"`
	Subject   string `json:"subject"`
	MessageID string `json:"message_id"`
	Date      string `json:"date"`
	Body      string `json:"body"`
}

// decodeMIMEHeader 解 RFC2047 编码的主题(=?UTF-8?B?...?=);解不了原样返回。
func decodeMIMEHeader(s string) string {
	if out, err := new(mime.WordDecoder).DecodeHeader(s); err == nil {
		return out
	}
	return s
}

// emailFingerprint 无 Message-ID 时的去重指纹。
func emailFingerprint(from, subject, date string) string {
	h := sha1.Sum([]byte(from + "|" + subject + "|" + date)) // #nosec G401 -- 去重指纹,非密码学用途
	return hex.EncodeToString(h[:8])
}
