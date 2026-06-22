// openapi.go OpenAPI spec 服务端点（能力域 14 开放 API）。
//
// 暴露：
//   - GET /openapi.yaml：由 swag v2 (--v3.1) 从代码注解生成的 OpenAPI 3.1 spec
//   - GET /docs：Swagger UI（在线 CDN 加载，无需前端依赖）
//
// spec 由 `go generate ./cmd/vigil/...` 重新生成至 internal/server/gen/swagger.yaml，
// 编译时 embed 进二进制；权威源是 handler 注解，而非本文件。
package server

import (
	_ "embed"
	"net/http"

	"github.com/labstack/echo/v5"
)

//go:embed gen/swagger.yaml
var openapiYAML string

// swaggerUI 是最小 Swagger UI HTML（从 CDN 加载，指向 /openapi.yaml）。
// swagger-ui-dist@5 原生支持 OpenAPI 3.1。
const swaggerUI = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <title>Vigil API 文档</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.onload = () => {
      SwaggerUIBundle({ url: "/openapi.yaml", dom_id: "#swagger-ui" });
    };
  </script>
</body>
</html>`

// registerOpenAPI 注册 OpenAPI spec + Swagger UI 路由（无需鉴权，文档公开）。
func (s *Server) registerOpenAPI() {
	s.echo.GET("/openapi.yaml", func(c *echo.Context) error {
		return c.Blob(http.StatusOK, "application/yaml", []byte(openapiYAML))
	})
	s.echo.GET("/docs", func(c *echo.Context) error {
		return c.HTML(http.StatusOK, swaggerUI)
	})
}
