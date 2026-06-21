// openapi.go OpenAPI spec 服务端点（能力域 14 开放 API）。
//
// 暴露：
//   - GET /openapi.yaml：原始 OpenAPI 3.0 spec（供前端/外部消费）
//   - GET /docs：Swagger UI（在线 CDN 加载，无需前端依赖）
//
// spec 文件随二进制 embed（docs/openapi.yaml 为权威源，编译时复制到本包 embed）。
package server

import (
	_ "embed"
	"net/http"

	"github.com/labstack/echo/v4"
)

//go:embed openapi.yaml
var openapiYAML string

// swaggerUI 是最小 Swagger UI HTML（从 CDN 加载，指向 /openapi.yaml）。
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
	s.echo.GET("/openapi.yaml", func(c echo.Context) error {
		return c.Blob(http.StatusOK, "application/yaml", []byte(openapiYAML))
	})
	s.echo.GET("/docs", func(c echo.Context) error {
		return c.HTML(http.StatusOK, swaggerUI)
	})
}
