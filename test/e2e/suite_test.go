//go:build integration

package e2e_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/kevin/vigil/internal/config"
	domainevent "github.com/kevin/vigil/internal/event"
	"github.com/kevin/vigil/internal/migrate"
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/server"
	"github.com/kevin/vigil/internal/store"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	_ "github.com/lib/pq" // 注册 postgres 驱动（ent dialect "postgres" 与 raw sql.DB 都依赖）
	"go.uber.org/zap"
)

// 全局测试环境：BeforeSuite 起一次，所有 Spec 共用同一个实例。
var (
	testEnv    *envState
	adminToken string
)

// envState 持有进程内启动的完整 Vigil 实例的访问入口。
// 直接持有 server/store/queue（生命周期管理 + DB 断言），不再经 app.App 聚合。
type envState struct {
	Server     *server.Server
	Store      *store.Store
	Queue      *queue.Queue
	baseURLStr string
	cancel     context.CancelFunc
}

// TestE2E 是 Ginkgo 的 Go test 入口。`go test` 通过它驱动整个 Ginkgo suite。
func TestE2E(t *testing.T) {
	// 先探测依赖是否可用，不可用则 skip 整个 suite（而非每个 spec 各自失败）。
	if !dependenciesAvailable() {
		t.Skip("e2e: postgres/redis not available (run 'make dev-up')")
		return
	}

	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Vigil E2E Suite")
}

// BeforeSuite：在整个 suite 运行前起一个完整的 Vigil 实例。
//
// 流程：预分配端口 → 覆盖配置 → 构造叶子依赖(store/queue/bus) → server.Wire 装配 →
// migrate 建表 → FlushDB 清 Redis → 启动 queue worker + http server → 轮询 /health 就绪。
// 默认管理员种子（admin/changeme）由 server.Wire 内 SeedDefaultAdmin 创建。
var _ = ginkgo.BeforeSuite(func() {
	ginkgo.By("启动 Vigil 实例（BeforeSuite）")

	// 1. 预分配空闲端口（先 listen 拿端口再关闭，交还给 server 用）
	addr, baseURL := allocateAddr()

	// 2. 覆盖配置（直接写 os.Setenv；BeforeSuite 生命周期覆盖整个 suite）
	// 连固定端口：由 docker-compose（本地）或 GitHub service container（CI）起的 PG/Redis。
	os.Setenv("VIGIL_DB_HOST", "localhost")
	os.Setenv("VIGIL_DB_PORT", "5432")
	os.Setenv("VIGIL_DB_USER", "vigil")
	os.Setenv("VIGIL_DB_PASSWORD", "vigil")
	os.Setenv("VIGIL_DB_NAME", "vigil")
	os.Setenv("VIGIL_DB_SSL_MODE", "disable")
	os.Setenv("VIGIL_REDIS_ADDR", "localhost:6379")
	// 用独立 Redis DB 15 隔离 e2e：asynq 是共享队列，若与本地 dev 服务（默认 DB 0）
	// 同库，会互相抢 normalize/triage 任务、用彼此配置处理，导致 e2e 间歇失败（任务被偷）。
	// 隔离到 15 后，e2e 只与自己的 worker 竞争，稳定可复现。BeforeSuite 会 FlushDB(15)。
	os.Setenv("VIGIL_REDIS_DB", "15")
	os.Setenv("VIGIL_HTTP_ADDR", addr)
	// 强制鉴权：e2e 验证 RBAC 与鉴权三轨，必须开启。
	os.Setenv("VIGIL_AUTH_ENABLED", "true")
	os.Setenv("VIGIL_AUTH_JWT_SECRET", "e2e-test-jwt-secret")
	os.Setenv("VIGIL_APP_ENV", "development")
	// 方案C：开启服务自动供给，供 service_autoprovision_test 验证端到端。
	// 仅当告警未路由 + 带 service/team label + 团队已配默认策略时才触发；
	// 其它 spec 不配团队默认策略，故不受影响（auto-provision 跳过 → 保持 unrouted）。
	os.Setenv("VIGIL_TRIAGE_AUTO_PROVISION_ENABLED", "true")

	cfg, err := config.Load()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "load config")

	// 3. 构造叶子依赖（store/queue/bus），贯穿 suite 生命周期的 ctx
	ctx, cancel := context.WithCancel(context.Background())
	log, _ := zap.NewDevelopment()

	st, err := store.New(ctx, cfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "open store")
	q := queue.New(cfg)
	bus := domainevent.New()

	// 4. 装配全部组件 + 路由（与生产同一套装配逻辑）
	wired, err := server.Wire(ctx, cfg, log, st, q, bus)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "wire app")

	// 5. 建表（幂等：已迁移会跳过；pre_0001_pgvector.sql 建 vector 扩展）
	gomega.Expect(migrate.Run(ctx, st.SQL, st.DB)).To(gomega.Succeed(), "migrate schema")

	// 6. 清空 Redis：保证 dedup key / 聚合器 / asynq 残留任务不污染 suite。
	gomega.Expect(st.Redis.FlushDB(ctx).Err()).To(gomega.Succeed(), "flush redis")

	// 7. 启动 queue worker（非阻塞，asynq 内建 goroutine）
	gomega.Expect(q.Start()).To(gomega.Succeed(), "start asynq worker")

	// 8. 启动 http server（goroutine）
	go func() {
		defer ginkgo.GinkgoRecover()
		if err := wired.Server.Start(); err != nil {
			// server 异常退出属致命，直接 Fail
			ginkgo.Fail("http server stopped: " + err.Error())
		}
	}()

	// 9. 轮询 /health 就绪
	env := &envState{Server: wired.Server, Store: st, Queue: q, baseURLStr: baseURL, cancel: cancel}
	waitHealthy(env)

	testEnv = env

	// 10. 登录默认管理员，缓存 token 供各 Spec 复用
	adminToken = loginAdmin(env)
})

// AfterSuite：suite 结束后优雅关闭（顺序与生产一致：queue → http → store）。
var _ = ginkgo.AfterSuite(func() {
	ginkgo.By("关闭 Vigil 实例（AfterSuite）")
	if testEnv == nil {
		return
	}
	testEnv.cancel()
	testEnv.Queue.Shutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = testEnv.Server.Shutdown(shutdownCtx)
	_ = testEnv.Store.Close()
	_ = testEnv.Queue.Close()
})

// allocateAddr 预分配一个空闲 TCP 端口，返回 (监听地址, baseURL)。
func allocateAddr() (addr, baseURL string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "allocate port")
	addr = ln.Addr().String()
	_ = ln.Close()
	return addr, "http://" + addr
}

// waitHealthy 轮询 /health 直到返回 200 或超时（10s）。
func waitHealthy(e *envState) {
	client := &http.Client{Timeout: 2 * time.Second}
	gomega.Eventually(func() int {
		resp, err := client.Get(e.baseURLStr + "/health")
		if err != nil {
			return 0
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}, 10*time.Second, 100*time.Millisecond).Should(gomega.Equal(http.StatusOK), "等待 /health 就绪")
}

// dependenciesAvailable 探测 PG + Redis 是否可达（连不上则整个 suite skip）。
// 比"每个 Spec 各自 bootstrap 失败再 skip"更高效，且日志更清晰。
func dependenciesAvailable() bool {
	// 用配置默认值探测（docker-compose 起的 localhost:5432 + localhost:6379）。
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	// 探测 PG（拨号 + 简单连通；不依赖 app.Bootstrap 的重装配）
	if !canDial("tcp", cfg.DB.Host+":"+strconv.Itoa(cfg.DB.Port)) {
		return false
	}
	if !canDial("tcp", cfg.Redis.Addr) {
		return false
	}
	return true
}

// canDial 探测 TCP 地址是否可达。
func canDial(network, addr string) bool {
	c, err := net.DialTimeout(network, addr, 2*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
