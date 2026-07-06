#!/usr/bin/env bash
# Vigil git 钩子共享库：被 pre-commit / commit-msg / pre-push 以 `. _lib.sh` 引入。
#
# 为什么单独抽库：三个钩子共用「跳过判定 + 彩色日志 + 工具探测 + 暂存文件枚举」，
# 抽出来避免复制粘贴导致行为漂移（docs/development.md §六：各 worktree 共享 hooks）。
# 本文件不是钩子（git 只按事件名 pre-commit/commit-msg/… 调用文件），故不会被直接执行。
#
# 兼容性：面向 macOS 自带 bash 3.2 —— 不用关联数组 / ${var,,} / mapfile。

# 彩色输出（仅当 stderr 是 TTY 时启用，避免污染 CI/管道日志）
if [ -t 2 ]; then
  _C_RED=$'\033[31m'; _C_GRN=$'\033[32m'; _C_YEL=$'\033[33m'; _C_DIM=$'\033[2m'; _C_RST=$'\033[0m'
else
  _C_RED=''; _C_GRN=''; _C_YEL=''; _C_DIM=''; _C_RST=''
fi

# 日志一律走 stderr：stdout 留给可能被 git 消费的钩子（如 pre-push 无），且不干扰重定向。
hook_info() { printf '%s\n' "${_C_DIM}[vigil-hook] $*${_C_RST}" >&2; }
hook_ok()   { printf '%s\n' "${_C_GRN}[vigil-hook] ✓ $*${_C_RST}" >&2; }
hook_warn() { printf '%s\n' "${_C_YEL}[vigil-hook] ⚠ $*${_C_RST}" >&2; }
hook_err()  { printf '%s\n' "${_C_RED}[vigil-hook] ✗ $*${_C_RST}" >&2; }

# 跳过总开关：VIGIL_SKIP_HOOKS=1（用于 CI、沙箱、应急）。
# 另：git 原生 `--no-verify` 会让 git 根本不调用 pre-commit/commit-msg/pre-push。
hook_skip_enabled() {
  case "${VIGIL_SKIP_HOOKS:-}" in
    1|true|yes|on|TRUE|YES|ON) return 0 ;;
    *) return 1 ;;
  esac
}

# 命令是否可用。
hook_has() { command -v "$1" >/dev/null 2>&1; }

# 枚举本次提交的暂存文件（Added/Copied/Modified/Renamed，排除删除），以 NUL 分隔输出，
# 兼容含空格/特殊字符的路径（调用方用 `while IFS= read -r -d '' f` 读取）。
hook_staged_files() {
  git diff --cached --name-only -z --diff-filter=ACMR
}
