// static.go 前端静态资源 + SPA history fallback（能力域 13 §前端 serve）。
//
// 将 web.DistFS（//go:embed web/dist）作为静态资源服务，并支持 SPA history 模式：
// 非文件请求（如刷新 /incidents/123）回退到 index.html，交前端路由处理。
//
// 挂在根级 echo，不走 /api/v1 group，不受 RBAC保护（静态资源无需鉴权）。
// 路由优先级 static > param > any（见 echo router.go）保证 /api/v1/*、/health、
// /metrics、/openapi.yaml、/docs、/ws/* 等具体路由优先命中，根级 /* 仅兜底。
package server

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/kevin/vigil/internal/web"

	"github.com/labstack/echo/v5"
)

// indexHTML 子 FS（dist 根）中的 SPA 入口文件名。
const indexHTML = "index.html"

// registerStatic 注册前端静态资源 + SPA fallback。
// 仅注册 GET（echo v5 Static 不自动镜像 HEAD；前端 SPA 场景只需 GET）。
//
// 实现策略：先 fs.Stat 判断请求路径是否对应真实文件，
//   - 是文件 → http.FileServer serve（含正确的 Content-Type / Range）
//   - 否（不存在或是目录）→ 返回 index.html，前端路由接管（SPA history mode）
//
// 用「先判断再 serve」而非「捕获 FileServer 的 404」，避免重复 WriteHeader 冲突，
// 也规避 echo.StaticDirectoryHandler 在 embed.FS 下返回 200 空体的异常行为。
func (s *Server) registerStatic() {
	// embed 的内容带 "dist/" 前缀，用 fs.Sub 切到 dist 根。
	distFS, err := fs.Sub(web.DistFS, "dist")
	if err != nil {
		// 理论不可达：//go:embed all:dist 保证 dist 目录存在。
		panic("server: embed dist sub fs: " + err.Error())
	}

	// 预读 index.html，SPA fallback 时直接写（避免每个请求重复读 FS）。
	indexBytes, err := fs.ReadFile(distFS, indexHTML)
	if err != nil {
		panic("server: embed dist missing index.html: " + err.Error())
	}

	fileServer := http.FileServer(http.FS(distFS))

	s.echo.GET("/*", func(c *echo.Context) error {
		p := strings.TrimPrefix(c.Param("*"), "/")

		// 判断是否真实文件：fs.Stat 对不存在文件/目录返回 err。
		// 仅当目标是「文件」时才走静态 serve；目录（含根）走 SPA fallback。
		fi, err := fs.Stat(distFS, p)
		if err == nil && !fi.IsDir() {
			// 真实文件：http.FileServer serve（重建完整 URL.Path 供其解析）。
			req := c.Request().Clone(c.Request().Context())
			req.URL.Path = "/" + p
			fileServer.ServeHTTP(c.Response(), req)
			return nil
		}

		// 非文件（不存在 / 目录 / 空路径）→ SPA fallback 到 index.html。
		return c.Blob(http.StatusOK, echo.MIMETextHTMLCharsetUTF8, indexBytes)
	})
}
