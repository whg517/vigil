#!/usr/bin/env bash
# 安装 Vigil git 钩子：把 core.hooksPath 指向仓库内 .githooks/。
#
# 为什么用 core.hooksPath 而非往 .git/hooks 拷文件：
#   - .githooks/ 入库，随仓库分发，团队一致、可 review、可版本化。
#   - core.hooksPath 写在共享的 .git/config，各 worktree 自动生效；相对路径由 git 在
#     「工作树根」下解析，故每个 worktree 用自己 checkout 的 .githooks 副本（docs §六）。
#
# 幂等，可重复运行。卸载：git config --unset core.hooksPath
# 用法：./scripts/install-hooks.sh   或   make install-hooks
set -euo pipefail

# 定位主工作树根（在 worktree 内运行也会配置到共享的 .git，全 worktree 生效）
ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

HOOKS_DIR=".githooks"
if [ ! -d "$HOOKS_DIR" ]; then
  echo "✗ 未找到 ${HOOKS_DIR}/，请在 Vigil 仓库根运行本脚本" >&2
  exit 1
fi

# 补齐可执行位（git 会保留 exec 位，但从压缩包/异常 checkout 落地时补一手更稳）
chmod +x "$HOOKS_DIR/pre-commit" "$HOOKS_DIR/commit-msg" "$HOOKS_DIR/pre-push" 2>/dev/null || true

git config core.hooksPath "$HOOKS_DIR"

echo "✓ 已启用 Vigil git 钩子（core.hooksPath=${HOOKS_DIR}）"
echo "    pre-commit : gofmt + go build 快检（按暂存范围，秒级）"
echo "    commit-msg : Conventional Commits 校验 + 拒绝 chore（docs §4）"
echo "    pre-push   : 完整门禁 golangci-lint + go test + build + 前端 lint/build（§3.4）"
echo "  临时跳过一次：git commit --no-verify / git push --no-verify"
echo "  全局跳过    ：export VIGIL_SKIP_HOOKS=1"
echo "  卸载        ：git config --unset core.hooksPath"
