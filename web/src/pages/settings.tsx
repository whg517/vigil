/**
 * Settings —— 设置页（能力域 13/3/7/8）。
 * 三个 tab：
 *   - IM 平台状态（飞书/钉钉 Available 只读）
 *   - RBAC（角色 + 角色绑定 CRUD）
 *   - 通知配置（规则 / 模板 / 抑制规则 CRUD）
 */
import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, KeyRound, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import {
  useAPIKeys,
  useAuditLogs,
  useCreateAPIKey,
  useCreateNotificationRule,
  useCreateNotificationTemplate,
  useCreateRole,
  useCreateRoleBinding,
  useCreateSuppressionRule,
  useDeleteAPIKey,
  useDeleteNotificationRule,
  useDeleteRole,
  useDeleteRoleBinding,
  useDeleteSuppressionRule,
  useDeleteNotificationTemplate,
  useNotificationRules,
  useNotificationTemplates,
  useRoles,
  useRoleBindings,
  useSuppressionRules,
} from "@/hooks/settings";
import { toast } from "sonner";
import { formatTime } from "@/lib/format";
import { useUsers } from "@/hooks/users-teams";
import { api } from "@/lib/api";
import type { RoleBinding } from "@/lib/types";

/** userName 从 RoleBinding.user edge 提取可读名（edge 带 [k:string]:unknown 索引，需收敛为 string）。 */
function userName(u: RoleBinding["user"]): string {
  if (!u) return "?";
  const name = String(u.name ?? u.username ?? "");
  return name || `用户#${u.id ?? "?"}`;
}

/** roleName 从 RoleBinding.role edge 提取可读名。 */
function roleName(r: RoleBinding["role"]): string {
  if (!r) return "角色#?";
  return String(r.name ?? "") || `角色#${r.id ?? "?"}`;
}

export function Settings() {
  const [tab, setTab] = useState("rbac");
  return (
    <div className="space-y-4 p-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">设置</h1>
        <p className="text-sm text-muted-foreground">平台配置：IM 平台、权限、通知规则。</p>
      </div>
      <Tabs
        value={tab}
        onValueChange={setTab}
        items={[
          { value: "rbac", label: "权限（RBAC）" },
          { value: "apikey", label: "API Key" },
          { value: "audit", label: "审计日志" },
          { value: "notification", label: "通知配置" },
          { value: "im", label: "IM 平台" },
        ]}
      />
      {tab === "rbac" && <RBACTab />}
      {tab === "apikey" && <APIKeyTab />}
      {tab === "audit" && <AuditTab />}
      {tab === "notification" && <NotificationTab />}
      {tab === "im" && <IMTab />}
    </div>
  );
}

// ===== IM 平台状态（只读）=====
/** 平台静态元数据（凭证提示 + 能力矩阵），与状态数据合并展示。 */
const IM_PLATFORM_META: Record<string, { label: string; env: string; capabilities: string }> = {
  feishu: {
    label: "飞书（Feishu）",
    env: "VIGIL_IM_FEISHU_APP_ID/APP_SECRET",
    capabilities: "交互卡片✅ 卡片更新✅ 建群✅ @人✅ 命令机器人✅",
  },
  dingtalk: {
    label: "钉钉（DingTalk）",
    env: "VIGIL_IM_DINGTALK_APP_KEY/APP_SECRET",
    capabilities: "交互卡片✅ 卡片更新⚠️（降级发新消息）建群✅ @人✅ 命令机器人✅",
  },
  wecom: {
    label: "企业微信（WeCom）",
    env: "（待 PoC）",
    capabilities: "占位适配器（NoopBot），未接入真实 API",
  },
};

/** IMTab 展示各 IM 平台适配器的实时就绪状态（GET /im/platforms）。凭证敏感，仅显示是否就绪。 */
function IMTab() {
  const qc = useQueryClient();
  const { data, isLoading, isError } = useQuery({
    queryKey: ["im-platforms"],
    queryFn: () => api.listIMPlatforms(),
    // 后端无凭证平台返回 available=false，状态稳定，低频刷新即可
    staleTime: 60_000,
  });

  const platforms = data ?? [];
  const readyCount = platforms.filter((p) => p.available).length;

  return (
    <div className="space-y-4">
      {/* 就绪总览 */}
      <Card>
        <CardContent className="flex flex-wrap items-center justify-between gap-3 p-4">
          <div className="text-sm text-muted-foreground">
            {isLoading ? (
              "加载平台状态…"
            ) : isError ? (
              <span className="text-destructive">平台状态查询失败，请检查后端 /im/platforms 接口。</span>
            ) : (
              <>
                共 {platforms.length} 个平台，<span className="font-medium text-foreground">{readyCount}</span> 个已就绪。
                凭证经环境变量配置，此处只读展示状态。
              </>
            )}
          </div>
          <Button
            size="sm"
            variant="outline"
            onClick={() => qc.invalidateQueries({ queryKey: ["im-platforms"] })}
          >
            刷新
          </Button>
        </CardContent>
      </Card>

      <div className="grid gap-3 md:grid-cols-2">
        {platforms.map((p) => {
          const meta = IM_PLATFORM_META[p.platform] ?? { label: p.platform, env: "—", capabilities: "—" };
          const ready = p.available;
          return (
            <Card key={p.platform}>
              <CardHeader className="flex-row items-center justify-between space-y-0">
                <CardTitle className="text-base">{meta.label}</CardTitle>
                <Badge variant={ready ? "default" : "secondary"}>
                  {ready ? "已就绪" : p.impl === "noop" ? "未接入" : "未配置"}
                </Badge>
              </CardHeader>
              <CardContent className="space-y-2 text-sm text-muted-foreground">
                <p>
                  配置环境变量 <code className="rounded bg-muted px-1">{meta.env}</code> 启用。
                </p>
                <p>能力：{meta.capabilities}</p>
                {!ready && p.impl !== "noop" && (
                  <p className="text-xs text-muted-foreground">
                    提示：修改 .env 后重启后端生效。
                  </p>
                )}
              </CardContent>
            </Card>
          );
        })}
        {platforms.length === 0 && !isLoading && !isError && (
          <Card>
            <CardContent className="p-6">
              <EmptyState title="无 IM 平台" description="后端未注册任何 IM 适配器。" />
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}

// ===== RBAC =====
function RBACTab() {
  const roles = useRoles();
  const bindings = useRoleBindings();
  const delRole = useDeleteRole();
  const delBinding = useDeleteRoleBinding();
  const [creatingRole, setCreatingRole] = useState(false);
  const [creatingBinding, setCreatingBinding] = useState(false);

  return (
    <div className="grid gap-4 md:grid-cols-2">
      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="text-base">角色</CardTitle>
          <Button size="sm" onClick={() => setCreatingRole(true)}>
            <Plus className="mr-1 h-4 w-4" /> 创建
          </Button>
        </CardHeader>
        <CardContent>
          {roles.isLoading ? (
            <Skeleton className="h-20 w-full" />
          ) : !roles.data || roles.data.length === 0 ? (
            <EmptyState title="无角色" description="创建自定义角色，组合权限点。" />
          ) : (
            <div className="space-y-2">
              {roles.data.map((r) => (
                <div key={r.id} className="flex items-center justify-between rounded-md border p-2">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium">{r.name}</span>
                      {r.builtin && <Badge variant="secondary" className="text-xs">内置</Badge>}
                      <Badge variant="outline" className="text-xs">{r.scope_level}</Badge>
                    </div>
                    <div className="mt-1 text-xs text-muted-foreground">
                      {r.permissions.length} 个权限点
                      {r.description ? ` · ${r.description}` : ""}
                    </div>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    disabled={r.builtin || delRole.isPending}
                    onClick={() => delRole.mutate(r.id)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="text-base">角色绑定（授权）</CardTitle>
          <Button size="sm" onClick={() => setCreatingBinding(true)}>
            <Plus className="mr-1 h-4 w-4" /> 授权
          </Button>
        </CardHeader>
        <CardContent>
          {bindings.isLoading ? (
            <Skeleton className="h-20 w-full" />
          ) : !bindings.data || bindings.data.length === 0 ? (
            <EmptyState title="无授权" description="给用户授予角色（含临时授权）。" />
          ) : (
            <div className="space-y-2">
              {bindings.data.map((b) => (
                <div key={b.id} className="flex items-center justify-between rounded-md border p-2 text-sm">
                  <div className="min-w-0">
                    <span className="font-medium">{userName(b.user)}</span>
                    <span className="mx-1 text-muted-foreground">→</span>
                    <span className="font-medium">{roleName(b.role)}</span>
                    <Badge variant="outline" className="ml-2 text-xs">{b.scope_level}</Badge>
                    {b.team_id && <span className="ml-2 text-xs text-muted-foreground">team #{b.team_id}</span>}
                    {b.expires_at && (
                      <Badge variant="secondary" className="ml-2 text-xs">临时 {formatTime(b.expires_at)}</Badge>
                    )}
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    disabled={delBinding.isPending}
                    onClick={() => delBinding.mutate(b.id)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      {creatingRole && <CreateRoleDialog onClose={() => setCreatingRole(false)} />}
      {creatingBinding && <CreateRoleBindingDialog onClose={() => setCreatingBinding(false)} />}
    </div>
  );
}

// 系统全部权限点（与 internal/auth/permission.go AllPermissions 对齐），按 resource 分组。
// 注：权限点为系统枚举，前端只做展示分组，新增权限点须同步后端 permission.go。
const ALL_PERMISSIONS = [
  "incident.view", "incident.create", "incident.ack", "incident.escalate", "incident.resolve",
  "incident.reopen", "incident.reassign", "incident.snooze", "incident.add_responder",
  "incident.runbook.execute", "incident.delete",
  "event.view", "event.view_unrouted",
  "service.view", "service.create", "service.update", "service.delete", "service.route_override",
  "schedule.view", "schedule.create", "schedule.update", "schedule.delete", "schedule.override",
  "escalation.view", "escalation.create", "escalation.update", "escalation.delete",
  "runbook.view", "runbook.create", "runbook.update", "runbook.delete", "runbook.execute",
  "integration.view", "integration.create", "integration.update", "integration.delete",
  "postmortem.view", "postmortem.create", "postmortem.update", "postmortem.publish", "postmortem.actionitem.manage",
  "team.view", "team.create", "team.update", "team.delete", "team.member.manage",
  "user.view", "user.create", "user.update", "user.disable", "user.im.bind",
  "role.view", "role.create", "role.update", "role.delete", "role.assign",
  "notification.rule.view", "notification.rule.create", "notification.rule.update", "notification.rule.delete",
  "notification.template.view", "notification.template.create", "notification.template.update", "notification.template.delete",
  "suppression.view", "suppression.create", "suppression.update", "suppression.delete",
  "admin.settings", "admin.audit.view", "admin.apikey.manage", "admin.global_integration",
];

/** CreateRoleDialog 创建自定义角色（权限点按 resource 分组多选）。 */
function CreateRoleDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateRole();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [scope, setScope] = useState<"org" | "team">("team");
  const [perms, setPerms] = useState<Set<string>>(new Set());

  // 按 resource 前缀分组：incident.* / service.* ...
  const groups = useMemo(() => {
    const m = new Map<string, string[]>();
    for (const p of ALL_PERMISSIONS) {
      const res = p.split(".")[0];
      const arr = m.get(res) ?? [];
      arr.push(p);
      m.set(res, arr);
    }
    return Array.from(m.entries()).sort((a, b) => a[0].localeCompare(b[0]));
  }, []);

  const togglePerm = (p: string) =>
    setPerms((prev) => {
      const next = new Set(prev);
      if (next.has(p)) next.delete(p);
      else next.add(p);
      return next;
    });

  const toggleGroup = (members: string[]) =>
    setPerms((prev) => {
      const allOn = members.every((m) => prev.has(m));
      const next = new Set(prev);
      if (allOn) members.forEach((m) => next.delete(m));
      else members.forEach((m) => next.add(m));
      return next;
    });

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate(
      { name, description, scope_level: scope, permissions: Array.from(perms) },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open onClose={onClose} title="创建角色" description="自由组合权限点。权限点为系统枚举（见 permission.go）。">
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">名称</label>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="oncall-responder" required autoFocus />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">作用域</label>
            <Select value={scope} onChange={(e) => setScope(e.target.value as "org" | "team")}>
              <option value="team">团队（team）</option>
              <option value="org">组织（org）</option>
            </Select>
          </div>
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">描述</label>
          <Input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="（可选）" />
        </div>
        <div className="space-y-1.5">
          <div className="flex items-center justify-between">
            <label className="text-sm font-medium">
              权限点 <span className="text-xs text-muted-foreground">（已选 {perms.size}）</span>
            </label>
          </div>
          <div className="max-h-64 space-y-2 overflow-auto rounded-md border p-2">
            {groups.map(([res, members]) => {
              const allOn = members.every((m) => perms.has(m));
              return (
                <div key={res}>
                  <button
                    type="button"
                    onClick={() => toggleGroup(members)}
                    className="flex w-full items-center gap-1 text-xs font-semibold uppercase text-muted-foreground hover:text-foreground"
                  >
                    <span>{res}</span>
                    <span className="font-normal normal-case opacity-60">({members.length})</span>
                    <span className="ml-auto">{allOn ? "取消全选" : "全选"}</span>
                  </button>
                  <div className="mt-1 flex flex-wrap gap-1">
                    {members.map((p) => {
                      const on = perms.has(p);
                      return (
                        <button
                          key={p}
                          type="button"
                          onClick={() => togglePerm(p)}
                          className={`rounded-md border px-2 py-0.5 text-xs transition-colors ${
                            on ? "border-primary bg-primary text-primary-foreground" : "hover:bg-accent"
                          }`}
                        >
                          {p.replace(`${res}.`, "")}
                        </button>
                      );
                    })}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>取消</Button>
          <Button type="submit" disabled={create.isPending || !name}>
            {create.isPending ? "创建中..." : "创建"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** CreateRoleBindingDialog 授权：选用户 → 选角色 → 作用域/有效期。 */
function CreateRoleBindingDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateRoleBinding();
  const { data: users } = useUsers();
  const { data: roles } = useRoles();
  const [userId, setUserId] = useState<number | undefined>(undefined);
  const [roleId, setRoleId] = useState<number | undefined>(undefined);
  const [scope, setScope] = useState<"org" | "team">("org");
  const [expiresIn, setExpiresIn] = useState("");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!userId || !roleId) return;
    create.mutate(
      { user_id: userId, role_id: roleId, scope_level: scope, expires_in_hours: expiresIn ? Number(expiresIn) : undefined },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open onClose={onClose} title="角色授权" description="把角色授予用户（可临时授权）。">
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">用户</label>
          <Select value={userId ? String(userId) : ""} onChange={(e) => setUserId(e.target.value ? Number(e.target.value) : undefined)}>
            <option value="">选择用户…</option>
            {users?.map((u) => (
              <option key={u.id} value={u.id}>
                {u.username || u.name || `用户#${u.id}`}（{u.email || "—"}）
              </option>
            ))}
          </Select>
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">角色</label>
          <Select value={roleId ? String(roleId) : ""} onChange={(e) => setRoleId(e.target.value ? Number(e.target.value) : undefined)}>
            <option value="">选择角色…</option>
            {roles?.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name}（{r.scope_level}）
              </option>
            ))}
          </Select>
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">作用域</label>
            <Select value={scope} onChange={(e) => setScope(e.target.value as "org" | "team")}>
              <option value="org">组织（org）</option>
              <option value="team">团队（team）</option>
            </Select>
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">有效期（小时，留空=永久）</label>
            <Input type="number" min={1} value={expiresIn} onChange={(e) => setExpiresIn(e.target.value)} placeholder="永久" />
          </div>
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>取消</Button>
          <Button type="submit" disabled={create.isPending || !userId || !roleId}>
            {create.isPending ? "授权中..." : "授权"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

// ===== 通知配置 =====
function NotificationTab() {
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
  const [creating, setCreating] = useState(false);
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
              <RuleRow key={r.id} name={r.name} enabled={r.enabled} meta={(r.channels || []).join(",")} onDelete={() => del.mutate(r.id)} deleting={del.isPending} />
            ))}
          </div>
        )}
      </CardContent>
      {creating && <CreateNotificationRuleDialog onClose={() => setCreating(false)} />}
    </Card>
  );
}

/** CreateNotificationRuleDialog 创建通知规则。channels 多选（im/email/phone/sms/webhook）。 */
function CreateNotificationRuleDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateNotificationRule();
  const [name, setName] = useState("");
  const [channels, setChannels] = useState<string[]>(["im"]);

  const toggleChan = (ch: string) => {
    setChannels((prev) => (prev.includes(ch) ? prev.filter((c) => c !== ch) : [...prev, ch]));
  };

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate(
      { name, channels, enabled: true, condition: {} },
      { onSuccess: onClose },
    );
  };

  const channelOptions = ["im", "email", "phone", "sms", "webhook"];

  return (
    <Dialog open onClose={onClose} title="创建通知规则" description="配置告警触达的通道与条件。">
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="默认通知" required autoFocus />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">通道（多选）</label>
          <div className="flex flex-wrap gap-2">
            {channelOptions.map((ch) => (
              <button
                key={ch}
                type="button"
                onClick={() => toggleChan(ch)}
                className={`rounded-md border px-3 py-1 text-sm transition-colors ${
                  channels.includes(ch) ? "border-primary bg-primary text-primary-foreground" : "hover:bg-accent"
                }`}
              >
                {ch}
              </button>
            ))}
          </div>
        </div>
        <Button type="submit" className="w-full" disabled={create.isPending || !name || channels.length === 0}>
          {create.isPending ? "创建中..." : "创建"}
        </Button>
      </form>
    </Dialog>
  );
}

function SuppressionRulesSection() {
  const { data, isLoading } = useSuppressionRules();
  const del = useDeleteSuppressionRule();
  const [creating, setCreating] = useState(false);
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
              />
            ))}
          </div>
        )}
      </CardContent>
      {creating && <CreateSuppressionRuleDialog onClose={() => setCreating(false)} />}
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

function TemplatesSection() {
  const { data, isLoading } = useNotificationTemplates();
  const del = useDeleteNotificationTemplate();
  const [creating, setCreating] = useState(false);
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
                  <Button variant="ghost" size="icon" disabled={del.isPending} onClick={() => del.mutate(t.id)}>
                    <Trash2 className="h-4 w-4" />
                  </Button>
                )}
              </div>
            ))}
          </div>
        )}
      </CardContent>
      {creating && <CreateNotificationTemplateDialog onClose={() => setCreating(false)} />}
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

/** RuleRow 规则行通用展示。 */
function RuleRow({
  name,
  enabled,
  meta,
  onDelete,
  deleting,
}: {
  name: string;
  enabled: boolean;
  meta: string;
  onDelete: () => void;
  deleting: boolean;
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
      <Button variant="ghost" size="icon" disabled={deleting} onClick={onDelete}>
        <Trash2 className="h-4 w-4" />
      </Button>
    </div>
  );
}

// ===== API Key（能力域 13 §API Key 管理）=====
/** APIKeyTab：列出/创建/撤销 API Key。创建时明文 token 仅展示一次，可复制。 */
function APIKeyTab() {
  const { data, isLoading } = useAPIKeys();
  const del = useDeleteAPIKey();
  const [creating, setCreating] = useState(false);

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">
          程序化接入凭证。请求带 <code className="rounded bg-muted px-1">X-Vigil-Key</code> 头即可鉴权。
        </p>
        <Button size="sm" onClick={() => setCreating(true)}>
          <KeyRound className="mr-1 h-4 w-4" /> 创建
        </Button>
      </div>
      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-20 w-full" />
          ) : !data || data.length === 0 ? (
            <EmptyState title="暂无 API Key" description="创建后用于程序化接入开放 API。" />
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">名称</th>
                  <th className="p-3">前缀</th>
                  <th className="p-3">状态</th>
                  <th className="p-3">最后使用</th>
                  <th className="p-3">创建时间</th>
                  <th className="p-3"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((k) => (
                  <tr key={k.id} className="border-b last:border-0">
                    <td className="p-3 font-medium">{k.name}</td>
                    <td className="p-3 font-mono text-xs text-muted-foreground">{k.prefix}…</td>
                    <td className="p-3">
                      <Badge variant={k.status === "active" ? "default" : "secondary"}>
                        {k.status}
                      </Badge>
                    </td>
                    <td className="p-3 text-muted-foreground">
                      {k.last_used_at ? formatTime(k.last_used_at) : "—"}
                    </td>
                    <td className="p-3 text-muted-foreground">{formatTime(k.created_at)}</td>
                    <td className="p-3 text-right">
                      <Button
                        size="icon"
                        variant="ghost"
                        disabled={del.isPending}
                        onClick={() => del.mutate(k.id)}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>
      {creating && <CreateAPIKeyDialog onClose={() => setCreating(false)} />}
    </div>
  );
}

/** CreateAPIKeyDialog 创建表单。成功后展示一次性明文 token + 复制按钮。 */
function CreateAPIKeyDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateAPIKey();
  const [name, setName] = useState("");
  const [expiresIn, setExpiresIn] = useState("");
  const [plaintext, setPlaintext] = useState<string | null>(null);

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const hours = expiresIn ? parseInt(expiresIn, 10) : undefined;
    create.mutate(
      { name, expires_in_hours: hours && hours > 0 ? hours : undefined },
      { onSuccess: (data) => setPlaintext(data.token) },
    );
  };

  const copyToken = async () => {
    if (!plaintext) return;
    try {
      await navigator.clipboard.writeText(plaintext);
      toast.success("已复制到剪贴板");
    } catch {
      toast.error("复制失败，请手动选择复制");
    }
  };

  // 创建成功后：展示一次性明文 token，不再显示表单
  if (plaintext) {
    return (
      <Dialog open onClose={onClose} title="API Key 已创建" description="⚠️ 明文 token 仅此一次展示，请立即复制保存，关闭后无法找回。">
        <div className="space-y-3">
          <div className="flex items-center gap-2 rounded-md border bg-muted p-3">
            <code className="flex-1 break-all text-xs">{plaintext}</code>
            <Button size="sm" variant="outline" onClick={copyToken}>
              <Copy className="mr-1 h-4 w-4" /> 复制
            </Button>
          </div>
          <Button className="w-full" onClick={onClose}>我已保存</Button>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog open onClose={onClose} title="创建 API Key" description="用于程序化接入（CI/CD、外部系统调 Vigil）。">
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="ci-deploy-key" required autoFocus />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">有效期（小时，留空=永久）</label>
          <Input
            value={expiresIn}
            onChange={(e) => setExpiresIn(e.target.value)}
            placeholder="720"
            type="number"
            min={1}
          />
        </div>
        <Button type="submit" className="w-full" disabled={create.isPending || !name}>
          {create.isPending ? "创建中..." : "创建"}
        </Button>
      </form>
    </Dialog>
  );
}

// ===== 审计日志（能力域 13 §审计日志，只读 + 筛选）=====
/** AuditTab：审计日志列表（倒序），按操作类型筛选。只读，无写操作。 */
function AuditTab() {
  const [action, setAction] = useState("");
  const { data, isLoading } = useAuditLogs(action ? { action, limit: 100 } : { limit: 100 });

  const resultBadge = (r: string) => {
    if (r === "success") return <Badge variant="default">{r}</Badge>;
    return <Badge variant="destructive">{r}</Badge>;
  };

  return (
    <div className="space-y-3">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="max-w-prose text-sm text-muted-foreground">
          敏感操作留痕（角色变更/API Key/登录等）。只读。
        </p>
        <div className="flex items-center gap-2">
          <label className="text-xs text-muted-foreground" htmlFor="audit-action-filter">筛选</label>
          <Select id="audit-action-filter" value={action} onChange={(e) => setAction(e.target.value)} className="w-44">
            <option value="">全部操作</option>
            <option value="role.create">角色创建</option>
            <option value="role.delete">角色删除</option>
            <option value="role.assign">角色授权</option>
            <option value="role.unassign">角色解权</option>
            <option value="apikey.create">API Key 创建</option>
            <option value="apikey.delete">API Key 撤销</option>
            <option value="auth.login">登录</option>
          </Select>
        </div>
      </div>
      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-32 w-full" />
          ) : !data || data.items.length === 0 ? (
            <EmptyState title="暂无审计日志" description="敏感操作会在此记录。" />
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">时间</th>
                  <th className="p-3">操作者</th>
                  <th className="p-3">操作</th>
                  <th className="p-3">对象</th>
                  <th className="p-3">结果</th>
                  <th className="p-3">IP</th>
                </tr>
              </thead>
              <tbody>
                {data.items.map((log) => (
                  <tr key={log.id} className="border-b last:border-0">
                    <td className="p-3 text-muted-foreground">{formatTime(log.created_at)}</td>
                    <td className="p-3">
                      <span className="font-medium">{log.actor_name || "—"}</span>
                      {log.actor_user_id > 0 && (
                        <span className="ml-1 text-xs text-muted-foreground">#{log.actor_user_id}</span>
                      )}
                    </td>
                    <td className="p-3 font-mono text-xs">{log.action}</td>
                    <td className="p-3 text-muted-foreground">
                      {log.resource_type}
                      {log.resource_name ? ` · ${log.resource_name}` : ""}
                    </td>
                    <td className="p-3">{resultBadge(log.result)}</td>
                    <td className="p-3 font-mono text-xs text-muted-foreground">{log.ip || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
