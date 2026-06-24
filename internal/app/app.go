// Package app 装配并持有 Vigil 的全部运行时组件。
//
// 它把 main.go 中"配置 → 构造各组件 → 挂载路由"的装配逻辑抽成可复用入口，
// 供生产入口（cmd/vigil）与集成测试共用同一套装配，避免两处漂移。
//
// 装配（Bootstrap）只负责构造与路由挂载，不启动阻塞型服务（HTTP server、Asynq worker）。
// 启动与优雅关闭由调用方按需驱动：生产入口控制信号监听与多组件有序关闭，
// 测试在进程内复用装配后自行决定何时启动/停止。
package app

import (
	"github.com/kevin/vigil/internal/config"
	"github.com/kevin/vigil/internal/queue"
	"github.com/kevin/vigil/internal/server"
	"github.com/kevin/vigil/internal/store"
	"github.com/kevin/vigil/internal/webhook"

	"go.uber.org/zap"
)

// App 聚合装配后需对外暴露的运行时组件。
//
// 只持有生命周期管理真正需要的引用：启动顺序为 queue → http，
// 关闭顺序相反（见 cmd/vigil/main.go）。各业务引擎（triage/escalation/
// incident/runbook/ai 等）在装配时已注入互连依赖、由各自 handler 持有，
// 不在此暴露——调用方只关心启动与关闭，不需要直接操作引擎。
type App struct {
	Cfg    *config.Config
	Log    *zap.Logger
	Store  *store.Store
	Queue  *queue.Queue
	Server *server.Server

	// WebhookDispatcher 出口分发器（能力域 14）。
	// main 在优雅关闭时 drain 在途推送；测试可用来断言订阅状态。
	WebhookDispatcher *webhook.Dispatcher
}
