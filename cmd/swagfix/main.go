// Package main 实现 swag 生成产物的后处理：对 OpenAPI schema 中重复的 enum 值去重。
//
// 背景：升级 Echo v5 后，其 binder_generic.go 引用了 time.Second/Nanosecond 等常量，
// 触发 swag 在依赖遍历时重复收集标准库 time 包的常量到 time.Duration 的 enum 数组
// （原本 8 个值被追加成 16 个）。又因 swag 内部用 map 收集、Go map 迭代序不确定，
// 导致每次 `swag init` 的产物顺序都不稳定——CI 的「重生成 + git diff」漂移门会随机失败。
//
// swag rc5 不提供关闭 enum 收集的开关，这里在生成后做确定性去重：遍历每个 schema，
// 保留 enum / x-enum-varnames 各自首次出现的元素（顺序稳定、可复现）。
//
// 处理三个产物文件（保持三者一致）：
//   - swagger.json   权威源，标准 encoding/json（UseNumber 保 int64 精度）
//   - docs.go        内嵌 docTemplate（反引号之间的 JSON 片段，含少量 Go text/template 动作）
//   - swagger.yaml   行级去重（保留 swag 原始格式，避免 yaml.v3 重序列化产生大 diff）
//
// 用法（见 cmd/vigil/generate.go 的 go:generate 链）：
//
//	go run ./cmd/swagfix ../../internal/server/gen
//
//nolint:gosec // G304: 路径来自 go:generate 指令（开发者控制），非外部输入，无路径注入风险。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: swagfix <gen-dir>")
		os.Exit(2)
	}
	dir := os.Args[1]

	for _, step := range []struct {
		name string
		fn   func(string) error
	}{
		{"swagger.json", processJSON},
		{"docs.go", processDocsGo},
		{"swagger.yaml", processYAML},
	} {
		path := filepath.Join(dir, step.name)
		if err := step.fn(path); err != nil {
			fmt.Fprintf(os.Stderr, "swagfix: %s: %v\n", step.name, err)
			os.Exit(1)
		}
	}
}

// --- swagger.json ---

// processJSON 读取 swagger.json，去重每个 schema 的 enum / x-enum-varnames，写回（4 空格缩进 + 末尾换行）。
//
// 关键：用 json.Decoder.UseNumber() 解码，让数字保留为 json.Number（原始字符串字面量），
// 而非默认的 float64——否则 int64 枚举值如 -9223372036854775808 会被四舍五入成
// -9223372036854776000（float64 精度仅 ~15 位有效数字），破坏 spec 正确性。
func processJSON(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	dedupeSchemasJSON(doc)
	out, err := json.MarshalIndent(doc, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

// dedupeSchemasJSON 在 components.schemas 中遍历每个 schema（map[string]any），去重其 enum 列表。
// enum 元素经 json.Unmarshal 后类型为 []any（元素可能是 float64/string），统一转 string 比较去重。
func dedupeSchemasJSON(doc map[string]any) {
	comps, _ := doc["components"].(map[string]any)
	schemas, _ := comps["schemas"].(map[string]any)
	for _, raw := range schemas {
		sch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, fld := range []string{"enum", "x-enum-varnames"} {
			if rawList, ok := sch[fld].([]any); ok {
				// 转 string 去重（保留首现），再写回。OpenAPI enum 的元素都是标量，
				// 用 fmt.Sprint 规范化比较 key 即可。
				seen := make(map[string]bool, len(rawList))
				out := rawList[:0]
				for _, v := range rawList {
					k := fmt.Sprint(v)
					if seen[k] {
						continue
					}
					seen[k] = true
					out = append(out, v)
				}
				sch[fld] = out
			}
		}
	}
}

// --- docs.go ---

var goTemplateAction = regexp.MustCompile(`\{\{[^}]*\}\}`)

// processDocsGo 处理 docs.go 内嵌的 docTemplate：
// 提取反引号之间的 JSON 主体 → 把 {{...}} 模板动作换成纯字母占位（合法 JSON 字符串）
// → 解析去重 → 还原占位 → 写回。
//
// docTemplate 主体是 JSON，但顶层值用 {{ marshal .X }} / {{escape .Description}} 等
// Go 模板动作（共 ~5 处），直接 json.Unmarshal 会失败。
func processDocsGo(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	src := string(data)

	const marker = "const docTemplate = `"
	start := strings.Index(src, marker)
	if start < 0 {
		return nil // 结构不符，保守跳过
	}
	start += len(marker)
	end := strings.Index(src[start:], "`")
	if end < 0 {
		return nil
	}
	end += start // 相对偏移转绝对偏移
	body := src[start:end]

	// 收集所有 {{...}}，替换成合法 JSON 占位。两种位置需区别对待：
	//   - 串内："{{...}}"  → "PLACEHOLDER"（外层引号已存在，只换内部）
	//   - 裸值：{{...}}    → "PLACEHOLDER"（补外层引号使其成为合法 JSON 字符串值）
	// 分两遍：先处理带引号的（避免把裸值的占位也卷进来），再处理剩余裸值的。
	var placeholders []string
	stash := func(m string) string {
		key := fmt.Sprintf("__SWAGFIXTPL%d__", len(placeholders))
		placeholders = append(placeholders, m)
		return key
	}
	// Pass 1：被引号包裹的 "{{...}}"，整体替换为 "占位"。
	quotedAction := regexp.MustCompile(`"\{\{[^}]*\}\}"`)
	swapped := quotedAction.ReplaceAllStringFunc(body, func(m string) string {
		// m 形如 "{{...}}"，去首尾引号后 stash，再包回引号。
		inner := m[1 : len(m)-1]
		return "\"" + stash(inner) + "\""
	})
	// Pass 2：剩余裸值 {{...}}，替换为 "占位"。
	swapped = goTemplateAction.ReplaceAllStringFunc(swapped, func(m string) string {
		return "\"" + stash(m) + "\""
	})

	var doc map[string]any
	dec := json.NewDecoder(strings.NewReader(swapped))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	dedupeSchemasJSON(doc)
	out, err := json.MarshalIndent(doc, "", "    ")
	if err != nil {
		return err
	}

	// 还原占位（按占位名倒序替换，避免 __SWAGFIXTPL1__ 误匹配 __SWAGFIXTPL10__）。
	result := string(out)
	for i := len(placeholders) - 1; i >= 0; i-- {
		key := fmt.Sprintf("__SWAGFIXTPL%d__", i)
		result = strings.ReplaceAll(result, key, placeholders[i])
	}

	return os.WriteFile(path, []byte(src[:start]+result+src[end:]), 0o600)
}

// --- swagger.yaml ---

// processYAML 对 swagger.yaml 做行级去重：在 enum / x-enum-varnames 列表块内，
// 跳过重复的标量行。
//
// 为什么用行级而非 yaml.v3 全量解析+重序列化：yaml.v3 的 Marshal 会重排 key 顺序、
// 改缩进风格，产生 ~11000 行 diff（全量重格式化），让 review 失去意义。行级处理只动
// 需要去重的行，保留 swag 原始格式，diff 极小且聚焦。JSON 路径已用正经解析器保证正确性，
// YAML 只是 JSON 的人类可读镜像，行级去重在此足够且 review 友好。
func processYAML(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))

	var (
		listField  string // 当前列表所属字段名（"enum" / "x-enum-varnames"），"" 表示不在目标列表
		listIndent = -1   // 列表项的缩进列
		seen       map[string]bool
	)
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		indent := len(line) - len(trimmed)

		if !strings.HasPrefix(trimmed, "- ") {
			// 非列表项：若已离开当前列表块（缩进回到父字段层级），重置上下文。
			if listField != "" && indent <= listIndent-2 {
				listField = ""
				seen = nil
			}
			// 检测新目标列表起始（字段名以冒号结尾）。
			if strings.HasSuffix(trimmed, ":") {
				key := strings.TrimSpace(strings.TrimSuffix(trimmed, ":"))
				if key == "enum" || key == "x-enum-varnames" {
					listField = key
					listIndent = -1 // 等到第一个 "- " 行才确定实际缩进
					seen = nil
				} else {
					listField = ""
					seen = nil
				}
			}
			out = append(out, line)
			continue
		}

		// 列表项 "- ..."
		if listField == "enum" || listField == "x-enum-varnames" {
			if listIndent < 0 {
				listIndent = indent
				seen = make(map[string]bool)
			}
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if seen[val] {
				continue // 跳过重复
			}
			seen[val] = true
		}
		out = append(out, line)
	}

	return os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o600)
}
