// Command verify-ai 独立验证 GLM Provider 的复盘起草效果。
// 不依赖 PG/Redis/HTTP 服务，直接调 AI Provider，验证 AI 能力本身。
// 用法：VIGIL_LLM_API_KEY=xxx go run ./cmd/verify-ai
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/kevin/vigil/internal/ai"
)

// 模拟一个真实事件 + 时间线（来自 e2e 测试场景）
func main() {
	key := os.Getenv("VIGIL_LLM_API_KEY")
	if key == "" {
		fmt.Println("请设置 VIGIL_LLM_API_KEY")
		os.Exit(1)
	}

	provider := ai.NewGLMProvider(key, "glm-4-flash", "")
	adapter := ai.NewPostmortemDraftAdapter(provider)

	// 模拟事件上下文（来自 e2e 测试场景）
	ctxMap := map[string]any{
		"title":    "支付服务5xx错误率超阈值",
		"severity": "critical",
		"summary":  "DB连接池耗尽导致支付5xx，持续30分钟",
		"timeline": []map[string]string{
			{"time": "10:28", "type": "incident_created", "content": "事件创建"},
			{"time": "10:29", "type": "escalated", "content": "升级 level 1 通知值班人"},
			{"time": "10:35", "type": "ack", "content": "DBA 张三确认接手"},
			{"time": "10:50", "type": "action", "content": "扩容 DB 连接池 100→500"},
			{"time": "10:58", "type": "resolved", "content": "5xx 恢复正常"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fmt.Println("========================================")
	fmt.Println("Vigil AI 复盘起草验证（智谱 GLM-4-Flash）")
	fmt.Println("========================================")
	fmt.Println("Provider Available:", provider.Available())
	fmt.Println()

	sections := []string{"summary", "impact", "root_cause"}
	for _, sec := range sections {
		fmt.Printf("【%s】\n", sec)
		draft, err := adapter.DraftSection(ctx, sec, ctxMap)
		if err != nil {
			fmt.Printf("  ❌ 失败: %v\n\n", err)
			continue
		}
		fmt.Printf("  %s\n\n", draft)
	}

	// 诊断验证：根因线索（带不确定性措辞 + JSON 输出）
	fmt.Println("========================================")
	fmt.Println("【根因诊断验证】")
	fmt.Println("========================================")
	diagPrompt := "你是运维根因分析助手。根据以下事件信息，推测可能的根因。\n" +
		"要求：\n1. 用不确定性措辞（\"可能\"\"疑似\"\"初步判断\"），绝不武断下结论\n" +
		"2. 输出必须是 JSON 格式：{\"root_cause\":\"...\",\"confidence\":0.0-1.0}\n\n" +
		"事件信息：\n- 标题：支付服务5xx错误率超阈值\n- 严重度：critical\n- 概要：DB连接池耗尽\n\n" +
		"时间线：\n- [10:28] incident_created: 事件创建\n- [10:29] escalated: 升级 level 1\n" +
		"- [10:35] ack: DBA 张三接手\n- [10:50] action: 扩容 DB 连接池 100→500\n- [10:58] resolved: 5xx 恢复正常\n"
	diagOut, err := provider.Complete(ctx, diagPrompt)
	if err != nil {
		fmt.Printf("  ❌ 失败: %v\n", err)
	} else {
		fmt.Printf("  %s\n", diagOut)
	}
}
