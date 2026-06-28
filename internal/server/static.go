// static.go 前端静态资源 + SPA history fallback（能力域 13 §前端 serve）。
//
// 将 web.DistFS（//go:embed web/dist）作为静态资源服务，并支持 SPA history 模式：
// 非文件请求（如刷新 /incidents/123）回退到 index.html，交前端路由处理。
//
// 挂在根级 echo，不走 /api/v1 group，不受 RBAC 保护（静态资源无需鉴权）。
// 路由优先级 static > param > any（见 echo router.go）保证 /api/v1/*、/health、
// /metrics、/openapi.yaml、/docs、/ws/* 等具体路由优先命中，根级 /* 仅兜底。
package server

import (
	"errors"
	"io/fs"
	"net/http"

	"github.com/kevin/vigil/internal/web"

	"github.com/labstack/echo/v5"
)

// indexHTML 子 FS（dist 根）中的 SPA 入口文件名。
const indexHTML = "index.html"

// registerStatic 注册前端静态资源 + SPA fallback。
// 仅注册 GET（echo v5 Static 不自动镜像 HEAD；前端 SPA 场景只需 GET）。
//
// 需在具体业务路由（/api/v1/*、/health 等）注册之后调用——虽路由优先级规则已保证
// /* 不会吞掉具体路由，但后注册语义更清晰，且便于阅读装配顺序。
func (s *Server) registerStatic() {
	// embed 的内容带 "dist/" 前缀，用 fs.Sub 切到 dist 根，使路径匹配干净
	// （请求 /assets/x.js 直接对应 dist 根下的 assets/x.js）。
	distFS, err := fs.Sub(web.DistFS, "dist")
	if err != nil {
		// 理论不可达：//go:embed all:dist 保证 dist 目录存在。与 New() 不返 error 的
		// 现有风格一致，此处 panic 避免静默跑在没有前端的环境上。
		panic("server: embed dist sub fs: " + err.Error())
	}

	// StaticDirectoryHandler(fs, disablePathUnescaping=true)：见 echo.go。
	// disablePathUnescaping 防止 %2f 等 URL 转义绕过路径校验（安全默认）。
	// 文件不存在时返回 echo.ErrNotFound，由 spaFallback 捕获回 index.html。
	fileHandler := echo.StaticDirectoryHandler(distFS, true)

	s.echo.GET("/*", func(c *echo.Context) error {
		if err := fileHandler(c); err == nil {
			return nil
		}
		// 文件不存在 → 返回 SPA 入口，让前端路由接管（history mode）。
		if errors.Is(err, echo.ErrNotFound) {
			return c.FileFS(indexHTML, distFS)
		}
		var he *echo.HTTPError
		if errors.As(err, &he) && he.Code == http.StatusNotFound {
			return c.FileFS(indexHTML, distFS)
		}
		return err
	})
}
