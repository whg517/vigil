<!-- PR 标题必须符合 Conventional Commits：<type>(<scope>): <subject>，禁止 chore（CI 会校验，squash 合并信息取自标题）。 -->

## 变更说明

<!-- 做了什么、为什么做。关联 issue 用 "Closes #123"。 -->

## 自查清单

参考仓库根目录 `CONTRIBUTING.md` 与 `docs/adr/0035-dev-workflow-gates.md`：

- [ ] **门禁本地全绿**：`golangci-lint run ./... && go test ./... && go build ./...` + `pnpm --dir web lint && pnpm --dir web build`
- [ ] **提交规范**：PR 标题与提交信息符合 Conventional Commits，未使用 `chore`
- [ ] **生成物已同步**（如适用）：改 `ent/schema/` 已 `go generate ./ent/...`；改 handler 注解已重生成 OpenAPI spec 与前端 types
- [ ] **e2e**（如适用）：改动涉及核心流水线（ingestion / triage / escalation / auth）已本地 `make test-e2e` 通过
- [ ] **文档同步**（如适用）：设计性改动已先落 ADR / `docs/architecture.md`（文档先行）

## 测试方式

<!-- 怎么验证这个改动：新增/修改了哪些测试，或手工验证步骤。 -->
