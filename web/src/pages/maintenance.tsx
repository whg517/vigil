/**
 * Maintenance —— 维护窗口独立操作流页（backlog §2.7，M3.2）。
 *
 * 维护窗口是 SuppressionRule 的一类（kind=maintenance），与日常抑制规则同实体、不同语义：
 * 为计划内变更划一段明确时间窗（time_window.{start,end} RFC3339），窗内命中告警自动抑制、
 * 到期自动失效（expires_at=end）。故独立成页，与「设置 → 抑制规则」分列，避免概念混淆。
 *
 * 后端：GET/POST/PATCH/DELETE /suppression-rules（?kind=maintenance 过滤列表）。
 */
import * as React from "react";
import { useState } from "react";
import { CalendarClock, Pencil, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useCreateSuppressionRule,
  useDeleteSuppressionRule,
  useMaintenanceWindows,
  useUpdateSuppressionRule,
} from "@/hooks/settings";
import { formatTime } from "@/lib/format";
import type { SuppressionRule } from "@/lib/types";

/** WindowState 依 now 与 time_window 计算的窗口状态。 */
type WindowState = "scheduled" | "active" | "expired" | "unknown";

/** readWindow 从 time_window（Record）读出 start/end 字符串（后端存 RFC3339）。 */
function readWindow(tw?: Record<string, unknown>): { start?: string; end?: string } {
  if (!tw) return {};
  const start = typeof tw.start === "string" ? tw.start : undefined;
  const end = typeof tw.end === "string" ? tw.end : undefined;
  return { start, end };
}

/** computeState 依当前时刻判断窗口状态：未开始=scheduled，进行中=active，已过=expired。 */
function computeState(tw?: Record<string, unknown>, now = Date.now()): WindowState {
  const { start, end } = readWindow(tw);
  if (!start || !end) return "unknown";
  const s = new Date(start).getTime();
  const e = new Date(end).getTime();
  if (Number.isNaN(s) || Number.isNaN(e)) return "unknown";
  if (now < s) return "scheduled";
  if (now > e) return "expired";
  return "active";
}

/** stateBadge 状态徽章（文案 + variant）。 */
function StateBadge({ state, endISO }: { state: WindowState; endISO?: string }) {
  if (state === "active") {
    return (
      <span className="inline-flex items-center gap-1.5">
        <Badge variant="triggered">生效中</Badge>
        {endISO && <span className="text-xs text-muted-foreground">{remainingText(endISO)}</span>}
      </span>
    );
  }
  if (state === "scheduled") return <Badge variant="acked">已排期</Badge>;
  if (state === "expired") return <Badge variant="closed">已结束</Badge>;
  return <Badge variant="secondary">—</Badge>;
}

/** remainingText 生效中窗口的剩余时间（简洁：剩 X 时 Y 分 / 剩 X 分）。 */
function remainingText(endISO: string): string {
  const ms = new Date(endISO).getTime() - Date.now();
  if (Number.isNaN(ms) || ms <= 0) return "";
  const mins = Math.floor(ms / 60000);
  if (mins < 60) return `剩 ${mins} 分`;
  const h = Math.floor(mins / 60);
  return `剩 ${h} 时 ${mins % 60} 分`;
}

/** isoToLocalInput 把 RFC3339/ISO 转为 datetime-local 输入值（本地时区，YYYY-MM-DDTHH:mm）。 */
function isoToLocalInput(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** localInputToISO 把 datetime-local 值（本地时区）转为 RFC3339/ISO（UTC，带 Z）。 */
function localInputToISO(local: string): string {
  // new Date("YYYY-MM-DDTHH:mm") 按本地时区解析，.toISOString() 输出 UTC ISO（含毫秒+Z），后端可解析。
  return new Date(local).toISOString();
}

export function Maintenance() {
  const { data, isLoading, isError } = useMaintenanceWindows();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<SuppressionRule | undefined>(undefined);

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">维护窗口</h1>
          <p className="text-sm text-muted-foreground">
            为计划内变更建维护窗，窗内命中告警自动抑制、到期自动失效。
          </p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> 新建维护窗口
        </Button>
      </div>

      <Card className="overflow-hidden">
        {isLoading ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-10 w-full" />
            ))}
          </div>
        ) : isError ? null : !data || data.length === 0 ? (
          <div className="p-6">
            <EmptyState
              icon={<CalendarClock className="h-8 w-8" />}
              title="暂无维护窗口"
              description="为计划内变更建维护窗，窗内告警自动抑制、到期自动失效。"
            />
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 text-left font-medium">名称</th>
                <th className="px-4 py-2.5 text-left font-medium">匹配范围</th>
                <th className="px-4 py-2.5 text-left font-medium">计划时间窗</th>
                <th className="px-4 py-2.5 text-left font-medium">状态</th>
                <th className="px-4 py-2.5 text-left font-medium">启用</th>
                <th className="px-4 py-2.5"></th>
              </tr>
            </thead>
            <tbody>
              {data.map((w) => (
                <WindowRow key={w.id} win={w} onEdit={() => setEditing(w)} />
              ))}
            </tbody>
          </table>
        )}
      </Card>

      {creating && <CreateWindowDialog onClose={() => setCreating(false)} />}
      {editing && <EditWindowDialog win={editing} onClose={() => setEditing(undefined)} />}
    </div>
  );
}

/** WindowRow 单行：范围/时间窗/状态/启停/编辑/删除。 */
function WindowRow({ win, onEdit }: { win: SuppressionRule; onEdit: () => void }) {
  const del = useDeleteSuppressionRule();
  const update = useUpdateSuppressionRule();
  const { start, end } = readWindow(win.time_window);
  const state = computeState(win.time_window);
  const labels = Object.entries(win.match_labels ?? {});

  return (
    <tr
      className={
        "border-b last:border-0 hover:bg-muted/30" + (state === "active" ? " bg-status-triggered/5" : "")
      }
    >
      <td className="px-4 py-3 font-medium">{win.name}</td>
      <td className="px-4 py-3">
        {labels.length > 0 ? (
          <div className="flex flex-wrap gap-1">
            {labels.map(([k, v]) => (
              <Badge key={k} variant="outline" className="font-mono text-xs">
                {k}={v}
              </Badge>
            ))}
          </div>
        ) : (
          <span className="text-xs text-muted-foreground">全部告警</span>
        )}
      </td>
      <td className="px-4 py-3 text-xs text-muted-foreground">
        {start && end ? (
          <span>
            {formatTime(start)} <span className="text-muted-foreground/60">~</span> {formatTime(end)}
          </span>
        ) : (
          "—"
        )}
      </td>
      <td className="px-4 py-3">
        <StateBadge state={state} endISO={end} />
      </td>
      <td className="px-4 py-3">
        <button
          type="button"
          onClick={() => update.mutate({ id: win.id, body: { enabled: !win.enabled } })}
          disabled={update.isPending}
          className="text-xs"
          title={win.enabled ? "点击停用" : "点击启用"}
        >
          <Badge variant={win.enabled ? "default" : "secondary"}>{win.enabled ? "启用" : "停用"}</Badge>
        </button>
      </td>
      <td className="px-4 py-3 text-right">
        <div className="flex items-center justify-end gap-1">
          <Button variant="ghost" size="icon" title="编辑" onClick={onEdit}>
            <Pencil className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            title="删除"
            onClick={() => {
              // 破坏性操作二次确认，防误删维护窗口
              if (window.confirm(`确认删除维护窗口「${win.name}」？`)) del.mutate(win.id);
            }}
            disabled={del.isPending}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </td>
    </tr>
  );
}

/** Field 表单字段（label + children）。 */
function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1">
      <span className="text-xs font-medium text-muted-foreground">{label}</span>
      {children}
    </label>
  );
}

/**
 * 维护窗口表单主体（创建/编辑共用）。
 * 计划起止用 datetime-local；提交时转 RFC3339，并把 expires_at 设为 end（窗口结束即自动失效）。
 */
interface WindowFormValues {
  name: string;
  matchKey: string;
  matchVal: string;
  startLocal: string;
  endLocal: string;
  severityFilter: string; // 逗号分隔 critical,warning
  preserveCritical: boolean;
}

function useWindowForm(initial?: SuppressionRule): [WindowFormValues, React.Dispatch<React.SetStateAction<WindowFormValues>>] {
  const { start, end } = readWindow(initial?.time_window);
  const labels = Object.entries(initial?.match_labels ?? {});
  return useState<WindowFormValues>({
    name: initial?.name ?? "",
    matchKey: labels[0]?.[0] ?? "",
    matchVal: labels[0]?.[1] ?? "",
    startLocal: isoToLocalInput(start),
    endLocal: isoToLocalInput(end),
    severityFilter: (initial?.severity_filter ?? []).join(","),
    preserveCritical: initial ? !!initial.preserve_critical : true,
  });
}

/**
 * buildBody 从表单值构造提交体。返回 { body } 或 { error }（校验失败）。
 * 关键：kind=maintenance；time_window={start,end}；expires_at=end（自动到期）。
 */
function buildBody(v: WindowFormValues): { body: Partial<SuppressionRule> } | { error: string } {
  if (!v.startLocal || !v.endLocal) return { error: "请填写计划起止时间" };
  const startISO = localInputToISO(v.startLocal);
  const endISO = localInputToISO(v.endLocal);
  if (new Date(startISO).getTime() >= new Date(endISO).getTime()) {
    return { error: "开始时间必须早于结束时间" };
  }
  const matchLabels: Record<string, string> = {};
  if (v.matchKey && v.matchVal) matchLabels[v.matchKey] = v.matchVal;
  const severityFilter = v.severityFilter
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);

  return {
    body: {
      name: v.name,
      kind: "maintenance",
      action: "suppress",
      match_labels: matchLabels,
      time_window: { start: startISO, end: endISO },
      // expires_at=end：窗口结束即自动失效（后端到期不再命中），实现「自动到期」。
      expires_at: endISO,
      severity_filter: severityFilter.length > 0 ? severityFilter : undefined,
      preserve_critical: v.preserveCritical,
    },
  };
}

/** WindowFields 共享的表单字段块。 */
function WindowFields({
  v,
  set,
}: {
  v: WindowFormValues;
  set: React.Dispatch<React.SetStateAction<WindowFormValues>>;
}) {
  return (
    <>
      <Field label="名称">
        <Input
          value={v.name}
          onChange={(e) => set((p) => ({ ...p, name: e.target.value }))}
          placeholder="支付服务发布窗口"
          required
          autoFocus
        />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="匹配 Label Key（留空=匹配全部）">
          <Input
            value={v.matchKey}
            onChange={(e) => set((p) => ({ ...p, matchKey: e.target.value }))}
            placeholder="service"
          />
        </Field>
        <Field label="匹配 Label Value">
          <Input
            value={v.matchVal}
            onChange={(e) => set((p) => ({ ...p, matchVal: e.target.value }))}
            placeholder="payment"
          />
        </Field>
      </div>
      <p className="text-xs text-muted-foreground">
        留空 Label 表示匹配全部告警（范围较大，请谨慎）。
      </p>
      <div className="grid grid-cols-2 gap-3">
        <Field label="计划开始">
          <Input
            type="datetime-local"
            value={v.startLocal}
            onChange={(e) => set((p) => ({ ...p, startLocal: e.target.value }))}
            required
          />
        </Field>
        <Field label="计划结束">
          <Input
            type="datetime-local"
            value={v.endLocal}
            onChange={(e) => set((p) => ({ ...p, endLocal: e.target.value }))}
            required
          />
        </Field>
      </div>
      <Field label="严重度过滤（逗号分隔，留空=全部）">
        <Input
          value={v.severityFilter}
          onChange={(e) => set((p) => ({ ...p, severityFilter: e.target.value }))}
          placeholder="warning,info"
        />
      </Field>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={v.preserveCritical}
          onChange={(e) => set((p) => ({ ...p, preserveCritical: e.target.checked }))}
          className="h-4 w-4"
        />
        <span>保护 critical（严重告警仍照常触达，不被窗口抑制）</span>
      </label>
    </>
  );
}

/** CreateWindowDialog 新建维护窗口。 */
function CreateWindowDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateSuppressionRule();
  const [v, set] = useWindowForm();
  const [err, setErr] = useState<string | undefined>(undefined);

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const r = buildBody(v);
    if ("error" in r) {
      setErr(r.error);
      return;
    }
    setErr(undefined);
    // enabled 默认 true：新建即生效（按 time_window 判定命中窗）。
    create.mutate({ ...r.body, name: v.name, enabled: true } as Parameters<typeof create.mutate>[0], {
      onSuccess: onClose,
    });
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title="新建维护窗口"
      description="窗内命中告警自动抑制，到期（结束时间）自动失效。"
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <WindowFields v={v} set={set} />
        {err && <p className="text-xs text-destructive">{err}</p>}
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" disabled={create.isPending || !v.name}>
            {create.isPending ? "创建中..." : "创建"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** EditWindowDialog 编辑维护窗口（时间窗/范围/严重度/保护 critical/启停）。 */
function EditWindowDialog({ win, onClose }: { win: SuppressionRule; onClose: () => void }) {
  const update = useUpdateSuppressionRule();
  const [v, set] = useWindowForm(win);
  const [enabled, setEnabled] = useState(!!win.enabled);
  const [err, setErr] = useState<string | undefined>(undefined);

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const r = buildBody(v);
    if ("error" in r) {
      setErr(r.error);
      return;
    }
    setErr(undefined);
    update.mutate(
      { id: win.id, body: { ...r.body, enabled } },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={`编辑维护窗口 · ${win.name}`}
      description="修改时间窗、匹配范围或启停。结束时间即到期失效点。"
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <WindowFields v={v} set={set} />
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            className="h-4 w-4"
          />
          <span>启用</span>
        </label>
        {err && <p className="text-xs text-destructive">{err}</p>}
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" disabled={update.isPending || !v.name}>
            {update.isPending ? "保存中..." : "保存"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
