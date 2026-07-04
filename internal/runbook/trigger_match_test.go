package runbook

import "testing"

// TestMatchTrigger_OnIncident on_incident 无条件命中（建单即展示）。
func TestMatchTrigger_OnIncident(t *testing.T) {
	trig := map[string]any{"type": "on_incident"}
	if !matchTrigger(trig, "info", nil) {
		t.Error("on_incident 应对任意 severity/label 命中")
	}
}

// TestMatchTrigger_Manual manual/未知类型不自动触发。
func TestMatchTrigger_Manual(t *testing.T) {
	for _, trig := range []map[string]any{
		{"type": "manual"},
		{"type": "wat"}, // 未知
		nil,             // 缺省
		{},              // 无 type
	} {
		if matchTrigger(trig, "critical", map[string]string{"a": "b"}) {
			t.Errorf("manual/未知 trigger 不应自动命中: %+v", trig)
		}
	}
}

// TestMatchTrigger_OnSeverity on_severity 按阈值（≥）命中。
func TestMatchTrigger_OnSeverity(t *testing.T) {
	cases := []struct {
		name      string
		trigger   map[string]any
		severity  string
		wantMatch bool
	}{
		{"critical>=warning 命中", map[string]any{"type": "on_severity", "condition": "severity >= warning"}, "critical", true},
		{"warning>=warning 命中", map[string]any{"type": "on_severity", "condition": "severity >= warning"}, "warning", true},
		{"info<warning 不命中", map[string]any{"type": "on_severity", "condition": "severity >= warning"}, "info", false},
		{"结构化 severity 字段", map[string]any{"type": "on_severity", "severity": "critical"}, "warning", false},
		{"结构化 severity 命中", map[string]any{"type": "on_severity", "severity": "warning"}, "critical", true},
		{"无阈值保守命中", map[string]any{"type": "on_severity"}, "info", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchTrigger(tc.trigger, tc.severity, nil); got != tc.wantMatch {
				t.Errorf("severity=%s want %v got %v", tc.severity, tc.wantMatch, got)
			}
		})
	}
}

// TestMatchTrigger_OnLabelMatch on_label_match 子集匹配。
func TestMatchTrigger_OnLabelMatch(t *testing.T) {
	trig := map[string]any{
		"type":   "on_label_match",
		"labels": map[string]any{"service": "payment", "env": "prod"},
	}
	cases := []struct {
		name      string
		labels    map[string]string
		wantMatch bool
	}{
		{"全命中（含多余标签）", map[string]string{"service": "payment", "env": "prod", "tier": "1"}, true},
		{"恰好命中", map[string]string{"service": "payment", "env": "prod"}, true},
		{"少一个标签不命中", map[string]string{"service": "payment"}, false},
		{"值不符不命中", map[string]string{"service": "payment", "env": "staging"}, false},
		{"空标签不命中", map[string]string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchTrigger(trig, "warning", tc.labels); got != tc.wantMatch {
				t.Errorf("labels=%+v want %v got %v", tc.labels, tc.wantMatch, got)
			}
		})
	}
}

// TestMatchTrigger_OnLabelMatch_NoLabelsUnrestricted trigger 无 labels 条件 → 不限，命中。
func TestMatchTrigger_OnLabelMatch_NoLabelsUnrestricted(t *testing.T) {
	trig := map[string]any{"type": "on_label_match"}
	if !matchTrigger(trig, "info", map[string]string{}) {
		t.Error("on_label_match 无 labels 条件应视为不限并命中")
	}
}

// TestTriggerType 类型提取（缺省归 manual）。
func TestTriggerType(t *testing.T) {
	if triggerType(nil) != triggerManual {
		t.Error("nil trigger 应归 manual")
	}
	if triggerType(map[string]any{"type": "on_incident"}) != triggerOnIncident {
		t.Error("应正确提取 on_incident")
	}
	if triggerType(map[string]any{}) != triggerManual {
		t.Error("无 type 应归 manual")
	}
}
