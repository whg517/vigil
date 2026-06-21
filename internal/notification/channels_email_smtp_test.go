package notification

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeSMTPServer 极简 SMTP mock：接受连接，回 250 应答，记录收到的 DATA 内容。
// 不做真实 SMTP 协议校验，只验证 SendMail 能走通 + 邮件正文正确。
type fakeSMTPServer struct {
	addr     string
	listener net.Listener
	mu       sync.Mutex
	messages []string // 收到的邮件正文（DATA 部分）
}

func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTPServer{listener: ln, addr: ln.Addr().String()}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeSMTPServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSMTPServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	writeLine := func(msg string) {
		_, _ = w.WriteString(msg + "\r\n")
		_ = w.Flush()
	}
	writeLine("220 fake-smtp ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			writeLine("250 OK")
		case strings.HasPrefix(upper, "MAIL FROM"), strings.HasPrefix(upper, "RCPT TO"):
			writeLine("250 OK")
		case strings.HasPrefix(upper, "AUTH"):
			writeLine("235 Authentication successful")
		case strings.HasPrefix(upper, "DATA"):
			writeLine("354 Start mail input")
			// 读取 DATA 内容直到单独的 . 行
			var sb strings.Builder
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimSpace(dl) == "." {
					break
				}
				sb.WriteString(dl)
			}
			s.mu.Lock()
			s.messages = append(s.messages, sb.String())
			s.mu.Unlock()
			writeLine("250 OK queued")
		case strings.HasPrefix(upper, "QUIT"):
			writeLine("221 Bye")
			return
		default:
			writeLine("250 OK")
		}
	}
}

func (s *fakeSMTPServer) hostPort() (string, int) {
	h, p, _ := net.SplitHostPort(s.addr)
	port, _ := strconv.Atoi(p)
	return h, port
}

// TestEmailChannel_RealSMTPSend 验证邮件真实发送（经 fakeSMTPServer）。
func TestEmailChannel_RealSMTPSend(t *testing.T) {
	srv := newFakeSMTPServer(t)
	host, port := srv.hostPort()

	ch := &EmailChannel{
		Config: SMTPConfig{Host: host, Port: port},
		From:   "vigil@test.com",
		GetEmails: func(targets []Target) []string {
			return []string{"alice@test.com", "bob@test.com"}
		},
	}
	if !ch.Available() {
		t.Fatal("configured email channel not available")
	}

	results, err := ch.Send(context.Background(), &Message{
		Incident: newPhoneIncident(),
		Title:    "[CRITICAL] 测试标题",
		Summary:  "测试正文内容",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if !r.Success {
			t.Errorf("result not success: %+v", r)
		}
	}
	// fakeSMTPServer 应收到 2 封邮件
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.messages) != 2 {
		t.Fatalf("fake smtp received %d messages, want 2", len(srv.messages))
	}
	// 邮件正文应含主题和正文
	first := srv.messages[0]
	if !strings.Contains(first, "Subject: [CRITICAL] 测试标题") {
		t.Errorf("email missing subject: %s", first)
	}
	if !strings.Contains(first, "测试正文内容") {
		t.Errorf("email missing body: %s", first)
	}
	if !strings.Contains(first, "To: alice@test.com") {
		t.Errorf("email missing To header: %s", first)
	}
}
