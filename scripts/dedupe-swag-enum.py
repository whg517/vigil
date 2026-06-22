#!/usr/bin/env python3
"""去重 swag 生成的 OpenAPI schema 中重复的 enum 值。

背景
----
升级 Echo v5 后，其 binder_generic.go 引用了 time.Second/Nanosecond 等常量，触发 swag
在依赖遍历时把标准库 time 包的常量重复收集到 time.Duration 的 enum 数组（8 个值被追加成 16
个）。又因 swag 内部用 map 收集、Go map 迭代序不确定，每次 `swag init` 的产物顺序都不同，
导致 CI 的「重生成 + git diff」漂移门随机失败。

swag rc5 不提供关闭 enum 收集的开关，这里在生成后做确定性去重：遍历每个 schema，保留
enum / x-enum-varnames 各自首次出现的元素（顺序稳定、可复现）。

处理三个文件（保持三者一致）：
  - swagger.json   权威源，JSON 反/序列化
  - docs.go        内嵌 docTemplate（反引号之间的 JSON 片段）
  - swagger.yaml   缩进文本，列表项行级去重

用法（见 cmd/vigil/generate.go 的 go:generate 链）：
  python3 scripts/dedupe-swag-enum.py internal/server/gen
"""
from __future__ import annotations

import json
import re
import sys
from pathlib import Path


def dedupe_seq(seq):
    """保留列表中每个元素首次出现，丢弃后续重复（顺序稳定）。"""
    if not isinstance(seq, list) or len(seq) <= 1:
        return seq
    seen = set()
    out = []
    for v in seq:
        # list 元素可能是 int/str/bool/None；统一用 repr 作 key。
        key = repr(v)
        if key in seen:
            continue
        seen.add(key)
        out.append(v)
    return out


def dedupe_schemas(schemas):
    """对 components.schemas 中每个 schema 的 enum / x-enum-varnames 去重（原地）。"""
    if not isinstance(schemas, dict):
        return
    for sch in schemas.values():
        if not isinstance(sch, dict):
            continue
        if "enum" in sch:
            sch["enum"] = dedupe_seq(sch["enum"])
        if "x-enum-varnames" in sch:
            sch["x-enum-varnames"] = dedupe_seq(sch["x-enum-varnames"])


def process_json(path: Path) -> None:
    """反序列化 swagger.json → 去重 → 写回（4 空格缩进 + 末尾换行，匹配 swag 风格）。"""
    doc = json.loads(path.read_text())
    dedupe_schemas(doc.get("components", {}).get("schemas"))
    path.write_text(json.dumps(doc, indent=4, ensure_ascii=False) + "\n")


def process_docs_go(path: Path) -> None:
    """处理 docs.go 内嵌的 docTemplate：提取反引号之间的内容、去重 schema enum、写回。

    docTemplate 是 Go text/template 字符串，主体是 JSON，但有少量 Go 模板动作 {{ ... }}，
    分两种位置：
      - 值位置（裸）：  "schemes": {{ marshal .Schemes }},   → 需替换为带引号占位
      - 串内位置：      "title": "{{.Title}}",               → 替换内部即可（外层引号保留）
    解析前统一替换成纯字母占位 token，去重后再还原。
    """
    src = path.read_text()
    marker = "const docTemplate = `"
    start = src.find(marker)
    if start < 0:
        return
    start += len(marker)
    end = src.find("`", start)
    if end < 0:
        return
    body = src[start:end]

    placeholders = {}

    def _stash(m):
        key = f"__SWAGFIXTPL{len(placeholders)}__"
        placeholders[key] = m.group(0)
        return key

    # 先处理串内动作："{{...}}" → "TOKEN"（保留外层引号，只换内部）。
    body = re.sub(r'(?<=": ")\{\{[^}]*\}\}(?=")', _stash, body)
    # 再处理裸值动作：{{...}} → "TOKEN"（补外层引号）。
    body = re.sub(r"\{\{[^}]*\}\}", lambda m: f'"{_stash(m)}"', body)

    doc = json.loads(body)
    dedupe_schemas(doc.get("components", {}).get("schemas"))
    out = json.dumps(doc, indent=4, ensure_ascii=False)

    # 还原：把每个纯 token 换回原始 {{...}} 动作（token 在 JSON 串内或裸值处均出现为纯文本）。
    for key, orig in placeholders.items():
        out = out.replace(key, orig)

    path.write_text(src[:start] + out + src[end:])


def process_yaml(path: Path) -> None:
    """swagger.yaml 行级去重：在 enum / x-enum-varnames 列表块内，跳过重复的标量行。

    YAML 列表项以 "- " 开头；通过跟踪「最近一个冒号结尾的字段名」界定列表归属。
    遇到缩进回到列表父字段层级或更浅的非列表行时，结束当前列表上下文。
    """
    lines = path.read_text().split("\n")
    out = []
    list_field = None      # 当前列表所属字段名（"enum" / "x-enum-varnames"），None 表示不在目标列表
    list_indent = -1       # 列表项的缩进列
    seen = None            # 当前列表已见值集合

    def field_indent_of(line):
        """返回该行去掉前导空白后的内容（用于判断字段名/列表项）。"""
        return line[len(line) - len(line.lstrip()):], line.lstrip()

    for line in lines:
        _, stripped = field_indent_of(line)
        indent = len(line) - len(stripped)

        if not stripped.startswith("- "):
            # 非列表项：若已离开当前列表块（缩进回到父字段层级），重置上下文。
            if list_field is not None and indent <= list_indent - 2:
                list_field = None
                seen = None
            # 检测新的目标列表起始（字段名以冒号结尾）。
            if stripped.endswith(":"):
                key = stripped[:-1].strip()
                if key in ("enum", "x-enum-varnames"):
                    list_field = key
                    list_indent = -1   # 等到第一个 "- " 行才确定实际缩进
                    seen = None
                else:
                    list_field = None
                    seen = None
            out.append(line)
            continue

        # 列表项 "- ..."
        if list_field in ("enum", "x-enum-varnames"):
            if list_indent < 0:
                list_indent = indent
                seen = set()
            val = stripped[2:].strip()
            if val in seen:
                continue  # 跳过重复
            seen.add(val)
        out.append(line)

    path.write_text("\n".join(out))


def main():
    if len(sys.argv) != 2:
        print("usage: dedupe-swag-enum.py <gen-dir>", file=sys.stderr)
        sys.exit(2)
    gen = Path(sys.argv[1])
    process_json(gen / "swagger.json")
    process_docs_go(gen / "docs.go")
    process_yaml(gen / "swagger.yaml")


if __name__ == "__main__":
    main()
