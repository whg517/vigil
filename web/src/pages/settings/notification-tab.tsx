/** 通知配置 —— NotificationTab：规则 / 抑制规则 / 模板 CRUD。 */
import { useState } from "react";
import { Pencil, Power, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import {
  useCreateNotificationRule,
  useCreateNotificationTemplate,
  useCreateSuppressionRule,
  useDeleteNotificationRule,
  useDeleteNotificationTemplate,
  useDeleteSuppressionRule,
  useNotificationRules,
  useNotificationTemplates,
  useSuppressionRules,
  useUpdateNotificationRule,
  useUpdateNotificationTemplate,
  useUpdateSuppressionRule,
} from "@/hooks/settings";
import type { NotificationRule, NotificationTemplate, SuppressionRule } from "@/lib/types";

export function NotificationTab() {
  return (
    <div className="space-y-4">
      <NotificationRulesSection />
      <SuppressionRulesSection />
      <TemplatesSection />
    </div>
  );
}

function NotificationRulesSection() {
  const { data, isLoading } = useNotificationRules();
  const del = useDeleteNotificationRule();
  const update = useUpdateNotificationRule();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<NotificationRule | undefined>(undefined);
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="text-base">通知规则</CardTitle>
        <Button size="sm" onClick={() => setCreating(true)}>创建</Button>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : !data || data.length === 0 ? (
          <EmptyState title="无通知规则" description="配置通道、模板与静默时段。" />
        ) : (
          <div className="space-y-2">
            {data.map((r) => (
              <RuleRow
                key={r.id}
                name={r.name}
                enabled={r.enabled}
                meta={(r.channels || []).join(",")}
                onDelete={() => del.mutate(r.id)}
                deleting={del.isPending}
                onEdit={() => setEditing(r)}
                onToggle={() => update.mutate({ id: r.id, body: { enabled: !r.enabled } })}
                updating={update.isPending}
              />
            ))}
          </div>
        )}
      </CardContent>
      {creating && <CreateNotificationRuleDialog onClose={() => setCreating(false)} />}
      {editing && <EditNotificationRuleDialog rule={editing} onClose={() => setEditing(undefined)} />}
    </Card>
  );
}

/** CreateNotificationRuleDialog 创建通知规则。channels 多选 + 条件（severity）+ 静默时段 + 绑定模板。 */
function CreateNotificationRuleDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateNotificationRule();
  const [name, setName] = useState("");
  const [channels, setChannels] = useState<string[]>(["im"]);
  const [severity, setSeverity] = useState(""); // 条件 severity，空=不限
  const [templateId, setTemplateId] = useState(""); // 绑定模板，空=用默认
  const [quiet, setQuiet] = useState<QuietHoursForm>(emptyQuietHours());

  const toggleChan = (ch: string) => {
    setChannels((prev) => (prev.includes(ch) ? prev.filter((c) => c !== ch) : [...prev, ch]));
  };

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const condition: Record<string, unknown> = {};
    if (severity) condition.severity = severity;
    create.mutate(
      {
        name,
        channels,
        enabled: true,
        condition,
        template_id: templateId || undefined,
        quiet_hours: buildQuietHours(quiet),
      },
      { onSuccess: onClose },
    );
  };

  const channelOptions = ["im", "email", "phone", "sms", "webhook"];

  return (
    <Dialog open onClose={onClose} title="创建通知规则" description="配置告警触达的通道、条件、静默时段与模板。">
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="默认通知" required autoFocus />
        </div>
        <ChannelSelector channels={channels} onToggle={toggleChan} options={channelOptions} />
        <SeverityConditionField value={severity} onChange={setSeverity} />
        <TemplateBindingField value={templateId} onChange={setTemplateId} />
        <QuietHoursFields value={quiet} onChange={setQuiet} />
        <Button type="submit" className="w-full" disabled={create.isPending || !name || channels.length === 0}>
          {create.isPending ? "创建中..." : "创建"}
        </Button>
      </form>
    </Dialog>
  );
}

/** EditNotificationRuleDialog 编辑通知规则（名称/通道/条件/静默时段/模板/启停）。 */
function EditNotificationRuleDialog({ rule, onClose }: { rule: NotificationRule; onClose: () => void }) {
  const update = useUpdateNotificationRule();
  const [name, setName] = useState(rule.name);
  const [enabled, setEnabled] = useState(!!rule.enabled);
  const [channels, setChannels] = useState<string[]>(rule.channels ?? []);
  // condition 拆出常用 severity 单独结构化编辑；其余键（team/service…）仍保留在 JSON 高级编辑。
  const [severity, setSeverity] = useState(() => {
    const s = (rule.condition ?? {}).severity;
    return typeof s === "string" ? s : "";
  });
  const [templateId, setTemplateId] = useState(rule.template_id ?? "");
  const [quiet, setQuiet] = useState<QuietHoursForm>(() => parseQuietHours(rule.quiet_hours));
  const [conditionText, setConditionText] = useState(() => {
    // 高级 JSON 编辑：去掉已由 severity 字段管理的键，避免重复。
    const rest = { ...(rule.condition ?? {}) };
    delete rest.severity;
    return Object.keys(rest).length === 0 ? "" : JSON.stringify(rest, null, 2);
  });
  const [condErr, setCondErr] = useState("");

  const toggleChan = (ch: string) => {
    setChannels((prev) => (prev.includes(ch) ? prev.filter((c) => c !== ch) : [...prev, ch]));
  };

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    let condition: Record<string, unknown> = {};
    if (conditionText.trim()) {
      try {
        const parsed = JSON.parse(conditionText);
        if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
          throw new Error("condition 必须是 JSON 对象");
        }
        condition = parsed as Record<string, unknown>;
      } catch (err) {
        setCondErr(err instanceof Error ? err.message : "JSON 解析失败");
        return;
      }
    }
    if (severity) condition.severity = severity;
    setCondErr("");
    update.mutate(
      {
        id: rule.id,
        body: {
          name,
          enabled,
          channels,
          condition,
          template_id: templateId || undefined,
          quiet_hours: buildQuietHours(quiet),
        },
      },
      { onSuccess: onClose },
    );
  };

  const channelOptions = ["im", "email", "phone", "sms", "webhook"];

  return (
    <Dialog open onClose={onClose} title={`编辑通知规则 · ${rule.name}`} description="修改通道、条件、静默时段、模板或启停。">
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        </div>
        <ChannelSelector channels={channels} onToggle={toggleChan} options={channelOptions} />
        <SeverityConditionField value={severity} onChange={setSeverity} />
        <TemplateBindingField value={templateId} onChange={setTemplateId} />
        <QuietHoursFields value={quiet} onChange={setQuiet} />
        <div className="space-y-1.5">
          <label className="text-sm font-medium">高级条件（JSON，team/service 等，留空=无）</label>
          <Textarea
            value={conditionText}
            onChange={(e) => setConditionText(e.target.value)}
            placeholder={`{"team":"sre","service":"payment"}`}
            className="min-h-[64px] font-mono text-xs"
          />
          {condErr && <p className="text-xs text-destructive">{condErr}</p>}
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} className="h-4 w-4" />
          <span>启用（停用后规则不再触发通知）</span>
        </label>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>取消</Button>
          <Button type="submit" disabled={update.isPending || !name}>
            {update.isPending ? "保存中..." : "保存"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

// ChannelSelector 通道多选（im/email/phone/sms/webhook）—— 规则创建/编辑共用。
function ChannelSelector({
  channels,
  onToggle,
  options,
}: {
  channels: string[];
  onToggle: (ch: string) => void;
  options: string[];
}) {
  return (
    <div className="space-y-1.5">
      <label className="text-sm font-medium">通道（多选）</label>
      <div className="flex flex-wrap gap-2">
        {options.map((ch) => (
          <button
            key={ch}
            type="button"
            onClick={() => onToggle(ch)}
            className={`rounded-md border px-3 py-1 text-sm transition-colors ${
              channels.includes(ch) ? "border-primary bg-primary text-primary-foreground" : "hover:bg-accent"
            }`}
          >
            {ch}
          </button>
        ))}
      </div>
    </div>
  );
}

// SeverityConditionField 触发条件 severity 单选（空=不限）。
function SeverityConditionField({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <div className="space-y-1.5">
      <label className="text-sm font-medium">触发条件 · 严重度（空=不限）</label>
      <Select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">不限</option>
        <option value="critical">critical</option>
        <option value="high">high</option>
        <option value="medium">medium</option>
        <option value="low">low</option>
        <option value="info">info</option>
      </Select>
    </div>
  );
}

// TemplateBindingField 绑定通知模板（按 name 引用；空=用默认模板）。
function TemplateBindingField({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const { data } = useNotificationTemplates();
  return (
    <div className="space-y-1.5">
      <label className="text-sm font-medium">绑定模板（空=默认）</label>
      <Select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">默认模板</option>
        {(data ?? []).map((t) => (
          <option key={t.id} value={t.name}>
            {t.name}（{t.channel}/{t.format}）
          </option>
        ))}
      </Select>
    </div>
  );
}

// —— 静默时段（quiet_hours）表单模型与转换 ——
interface QuietHoursForm {
  enabled: boolean;
  start: string; // HH:MM
  end: string; // HH:MM
  timezone: string;
  bypassCritical: boolean; // critical 穿透静默
}

function emptyQuietHours(): QuietHoursForm {
  return { enabled: false, start: "22:00", end: "07:00", timezone: "Asia/Shanghai", bypassCritical: true };
}

// parseQuietHours 从后端 quiet_hours JSON 回填表单。
function parseQuietHours(raw?: Record<string, unknown>): QuietHoursForm {
  const q = raw ?? {};
  const bypass = Array.isArray(q.bypass_for) ? (q.bypass_for as unknown[]).map(String) : [];
  return {
    enabled: !!q.enabled,
    start: typeof q.start === "string" && q.start ? q.start : "22:00",
    end: typeof q.end === "string" && q.end ? q.end : "07:00",
    timezone: typeof q.timezone === "string" && q.timezone ? q.timezone : "Asia/Shanghai",
    // 未配 bypass_for 时后端默认 critical 穿透；空数组也按默认呈现为勾选。
    bypassCritical: bypass.length === 0 ? true : bypass.some((b) => b.toLowerCase() === "critical"),
  };
}

// buildQuietHours 表单 → 后端 quiet_hours JSON。未启用则返回 { enabled:false }（清空静默）。
function buildQuietHours(f: QuietHoursForm): Record<string, unknown> {
  if (!f.enabled) return { enabled: false };
  return {
    enabled: true,
    start: f.start,
    end: f.end,
    timezone: f.timezone,
    bypass_for: f.bypassCritical ? ["critical"] : [],
  };
}

// QuietHoursFields 静默时段编辑（启停 + 起止 HH:MM + 时区 + critical 穿透）。
function QuietHoursFields({
  value,
  onChange,
}: {
  value: QuietHoursForm;
  onChange: (v: QuietHoursForm) => void;
}) {
  const patch = (p: Partial<QuietHoursForm>) => onChange({ ...value, ...p });
  return (
    <div className="space-y-2 rounded-md border p-3">
      <label className="flex items-center gap-2 text-sm font-medium">
        <input
          type="checkbox"
          checked={value.enabled}
          onChange={(e) => patch({ enabled: e.target.checked })}
          className="h-4 w-4"
        />
        <span>静默时段（少打扰，值班人与 critical 不受限）</span>
      </label>
      {value.enabled && (
        <div className="space-y-2">
          <div className="grid grid-cols-3 gap-2">
            <div className="space-y-1">
              <label className="text-xs text-muted-foreground">开始（HH:MM）</label>
              <Input value={value.start} onChange={(e) => patch({ start: e.target.value })} placeholder="22:00" />
            </div>
            <div className="space-y-1">
              <label className="text-xs text-muted-foreground">结束（HH:MM）</label>
              <Input value={value.end} onChange={(e) => patch({ end: e.target.value })} placeholder="07:00" />
            </div>
            <div className="space-y-1">
              <label className="text-xs text-muted-foreground">时区</label>
              <Input value={value.timezone} onChange={(e) => patch({ timezone: e.target.value })} placeholder="Asia/Shanghai" />
            </div>
          </div>
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={value.bypassCritical}
              onChange={(e) => patch({ bypassCritical: e.target.checked })}
              className="h-4 w-4"
            />
            <span>critical 穿透静默（严重告警始终通知）</span>
          </label>
          <p className="text-xs text-muted-foreground">支持跨午夜（如 22:00 → 07:00）。</p>
        </div>
      )}
    </div>
  );
}

function SuppressionRulesSection() {
  const { data, isLoading } = useSuppressionRules();
  const del = useDeleteSuppressionRule();
  const update = useUpdateSuppressionRule();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<SuppressionRule | undefined>(undefined);
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="text-base">抑制规则（少打扰）</CardTitle>
        <Button size="sm" onClick={() => setCreating(true)}>创建</Button>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : !data || data.length === 0 ? (
          <EmptyState title="无抑制规则" description="满足条件时主动抑制（维护窗口/已知问题）。" />
        ) : (
          <div className="space-y-2">
            {data.map((r) => (
              <RuleRow
                key={r.id}
                name={r.name}
                enabled={r.enabled}
                meta={`${r.action}${r.preserve_critical ? "·保护critical" : ""}`}
                onDelete={() => del.mutate(r.id)}
                deleting={del.isPending}
                onEdit={() => setEditing(r)}
                onToggle={() => update.mutate({ id: r.id, body: { enabled: !r.enabled } })}
                updating={update.isPending}
              />
            ))}
          </div>
        )}
      </CardContent>
      {creating && <CreateSuppressionRuleDialog onClose={() => setCreating(false)} />}
      {editing && <EditSuppressionRuleDialog rule={editing} onClose={() => setEditing(undefined)} />}
    </Card>
  );
}

/** CreateSuppressionRuleDialog 创建抑制规则。action: suppress/reduce_severity。 */
function CreateSuppressionRuleDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateSuppressionRule();
  const [name, setName] = useState("");
  const [action, setAction] = useState<"suppress" | "reduce_severity">("suppress");
  const [matchLabelKey, setMatchLabelKey] = useState("");
  const [matchLabelVal, setMatchLabelVal] = useState("");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const matchLabels: Record<string, string> = {};
    if (matchLabelKey && matchLabelVal) {
      matchLabels[matchLabelKey] = matchLabelVal;
    }
    create.mutate(
      { name, action, match_labels: matchLabels, enabled: true },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open onClose={onClose} title="创建抑制规则" description="满足条件时抑制或降级告警（维护窗口/已知问题）。">
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="维护窗口抑制" required autoFocus />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">动作</label>
          <Select value={action} onChange={(e) => setAction(e.target.value as "suppress" | "reduce_severity")}>
            <option value="suppress">抑制（suppress）</option>
            <option value="reduce_severity">降级（reduce_severity）</option>
          </Select>
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">匹配 Label Key</label>
            <Input value={matchLabelKey} onChange={(e) => setMatchLabelKey(e.target.value)} placeholder="env" />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">匹配 Label Value</label>
            <Input value={matchLabelVal} onChange={(e) => setMatchLabelVal(e.target.value)} placeholder="staging" />
          </div>
        </div>
        <p className="text-xs text-muted-foreground">留空 Label 表示匹配所有（仅靠 action 控制）。</p>
        <Button type="submit" className="w-full" disabled={create.isPending || !name}>
          {create.isPending ? "创建中..." : "创建"}
        </Button>
      </form>
    </Dialog>
  );
}

/** EditSuppressionRuleDialog 编辑抑制规则（动作/匹配 Label/保护 critical/启停）。 */
function EditSuppressionRuleDialog({ rule, onClose }: { rule: SuppressionRule; onClose: () => void }) {
  const update = useUpdateSuppressionRule();
  const [name, setName] = useState(rule.name);
  const [enabled, setEnabled] = useState(!!rule.enabled);
  const [action, setAction] = useState<"suppress" | "reduce_severity">(rule.action ?? "suppress");
  const [preserveCritical, setPreserveCritical] = useState(!!rule.preserve_critical);

  // match_labels 简化为单条 key/val 行编辑（与创建表单一致），空则匹配所有。
  const labels = Object.entries(rule.match_labels ?? {});
  const [labelKey, setLabelKey] = useState(labels[0]?.[0] ?? "");
  const [labelVal, setLabelVal] = useState(labels[0]?.[1] ?? "");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const next: Record<string, string> = labelKey && labelVal ? { [labelKey]: labelVal } : {};
    update.mutate(
      { id: rule.id, body: { name, enabled, action, match_labels: next, preserve_critical: preserveCritical } },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open onClose={onClose} title={`编辑抑制规则 · ${rule.name}`} description="修改动作、匹配条件或启停。">
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">动作</label>
          <Select value={action} onChange={(e) => setAction(e.target.value as "suppress" | "reduce_severity")}>
            <option value="suppress">抑制（suppress）</option>
            <option value="reduce_severity">降级（reduce_severity）</option>
          </Select>
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">匹配 Label Key</label>
            <Input value={labelKey} onChange={(e) => setLabelKey(e.target.value)} placeholder="env" />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">匹配 Label Value</label>
            <Input value={labelVal} onChange={(e) => setLabelVal(e.target.value)} placeholder="staging" />
          </div>
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={preserveCritical}
            onChange={(e) => setPreserveCritical(e.target.checked)}
            className="h-4 w-4"
          />
          <span>保护 critical（严重告警不抑制）</span>
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} className="h-4 w-4" />
          <span>启用</span>
        </label>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>取消</Button>
          <Button type="submit" disabled={update.isPending || !name}>
            {update.isPending ? "保存中..." : "保存"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function TemplatesSection() {
  const { data, isLoading } = useNotificationTemplates();
  const del = useDeleteNotificationTemplate();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<NotificationTemplate | undefined>(undefined);
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="text-base">通知模板</CardTitle>
        <Button size="sm" onClick={() => setCreating(true)}>创建</Button>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : !data || data.length === 0 ? (
          <EmptyState title="无模板" description="内置默认模板已 seed，可自定义覆盖。" />
        ) : (
          <div className="space-y-2">
            {data.map((t) => (
              <div key={t.id} className="flex items-center justify-between rounded-md border p-2 text-sm">
                <div>
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{t.name}</span>
                    <Badge variant="outline" className="text-xs">{t.channel}/{t.format}</Badge>
                    {t.builtin && <Badge variant="secondary" className="text-xs">内置</Badge>}
                  </div>
                </div>
                {!t.builtin && (
                  <div className="flex items-center gap-1">
                    <Button
                      variant="ghost"
                      size="icon"
                      title="编辑"
                      disabled={del.isPending}
                      onClick={() => setEditing(t)}
                    >
                      <Pencil className="h-4 w-4" />
                    </Button>
                    <Button variant="ghost" size="icon" disabled={del.isPending} onClick={() => del.mutate(t.id)}>
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </CardContent>
      {creating && <CreateNotificationTemplateDialog onClose={() => setCreating(false)} />}
      {editing && <EditTemplateDialog template={editing} onClose={() => setEditing(undefined)} />}
    </Card>
  );
}

/** CreateNotificationTemplateDialog 创建通知模板。channel/format/title/body。 */
function CreateNotificationTemplateDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateNotificationTemplate();
  const [name, setName] = useState("");
  const [channel, setChannel] = useState<"im" | "email" | "webhook" | "phone" | "sms">("im");
  const [format, setFormat] = useState<"text" | "interactive_card">("text");
  const [titleTemplate, setTitleTemplate] = useState("");
  const [bodyTemplate, setBodyTemplate] = useState("");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate(
      { name, channel, format, title_template: titleTemplate, body_template: bodyTemplate },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open onClose={onClose} title="创建通知模板" description="Go template 语法渲染标题与正文。">
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称（唯一标识）</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="custom_im_card" required autoFocus />
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">通道</label>
            <Select value={channel} onChange={(e) => setChannel(e.target.value as "im" | "email" | "webhook" | "phone" | "sms")}>
              <option value="im">im</option>
              <option value="email">email</option>
              <option value="webhook">webhook</option>
              <option value="phone">phone</option>
              <option value="sms">sms</option>
            </Select>
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">格式</label>
            <Select value={format} onChange={(e) => setFormat(e.target.value as "text" | "interactive_card")}>
              <option value="text">text（纯文本）</option>
              <option value="interactive_card">interactive_card（IM 卡片）</option>
            </Select>
          </div>
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">标题模板</label>
          <Input value={titleTemplate} onChange={(e) => setTitleTemplate(e.target.value)} placeholder={`[{{.Severity}}] {{.Number}}`} />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">正文模板</label>
          <Textarea value={bodyTemplate} onChange={(e) => setBodyTemplate(e.target.value)} placeholder={`事件: {{.Summary}}\n负责人: {{.Responder}}`} className="min-h-[80px]" />
        </div>
        <Button type="submit" className="w-full" disabled={create.isPending || !name}>
          {create.isPending ? "创建中..." : "创建"}
        </Button>
      </form>
    </Dialog>
  );
}

/** EditTemplateDialog 编辑通知模板（名称/通道/格式/标题/正文）。 */
function EditTemplateDialog({ template, onClose }: { template: NotificationTemplate; onClose: () => void }) {
  const update = useUpdateNotificationTemplate();
  const [name, setName] = useState(template.name);
  const [channel, setChannel] = useState<"im" | "email" | "webhook" | "phone" | "sms">(template.channel ?? "im");
  const [format, setFormat] = useState<"text" | "interactive_card">(template.format ?? "text");
  const [titleTemplate, setTitleTemplate] = useState(template.title_template ?? "");
  const [bodyTemplate, setBodyTemplate] = useState(template.body_template ?? "");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    update.mutate(
      { id: template.id, body: { name, channel, format, title_template: titleTemplate, body_template: bodyTemplate } },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open onClose={onClose} title={`编辑通知模板 · ${template.name}`} description="Go template 语法渲染标题与正文。">
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称（唯一标识）</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">通道</label>
            <Select value={channel} onChange={(e) => setChannel(e.target.value as "im" | "email" | "webhook" | "phone" | "sms")}>
              <option value="im">im</option>
              <option value="email">email</option>
              <option value="webhook">webhook</option>
              <option value="phone">phone</option>
              <option value="sms">sms</option>
            </Select>
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">格式</label>
            <Select value={format} onChange={(e) => setFormat(e.target.value as "text" | "interactive_card")}>
              <option value="text">text（纯文本）</option>
              <option value="interactive_card">interactive_card（IM 卡片）</option>
            </Select>
          </div>
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">标题模板</label>
          <Input value={titleTemplate} onChange={(e) => setTitleTemplate(e.target.value)} placeholder={`[{{.Severity}}] {{.Number}}`} />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">正文模板</label>
          <Textarea value={bodyTemplate} onChange={(e) => setBodyTemplate(e.target.value)} placeholder={`事件: {{.Summary}}\n负责人: {{.Responder}}`} className="min-h-[80px]" />
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>取消</Button>
          <Button type="submit" disabled={update.isPending || !name}>
            {update.isPending ? "保存中..." : "保存"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** RuleRow 规则行通用展示。支持行内启停与编辑（onEdit/onToggle 可选）。 */
function RuleRow({
  name,
  enabled,
  meta,
  onDelete,
  deleting,
  onEdit,
  onToggle,
  updating,
}: {
  name: string;
  enabled: boolean;
  meta: string;
  onDelete: () => void;
  deleting: boolean;
  onEdit?: () => void;
  onToggle?: () => void;
  updating?: boolean;
}) {
  return (
    <div className="flex items-center justify-between rounded-md border p-2">
      <div>
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium">{name}</span>
          <Badge variant={enabled ? "default" : "secondary"} className="text-xs">
            {enabled ? "启用" : "停用"}
          </Badge>
        </div>
        <div className="mt-1 text-xs text-muted-foreground">{meta}</div>
      </div>
      <div className="flex items-center gap-1">
        {onToggle && (
          <Button
            variant="ghost"
            size="icon"
            title={enabled ? "停用" : "启用"}
            disabled={updating}
            onClick={onToggle}
          >
            <Power className="h-4 w-4" />
          </Button>
        )}
        {onEdit && (
          <Button variant="ghost" size="icon" title="编辑" disabled={updating} onClick={onEdit}>
            <Pencil className="h-4 w-4" />
          </Button>
        )}
        <Button variant="ghost" size="icon" disabled={deleting} onClick={onDelete}>
          <Trash2 className="h-4 w-4" />
        </Button>
      </div>
    </div>
  );
}
