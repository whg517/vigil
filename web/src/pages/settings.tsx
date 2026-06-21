/**
 * Settings —— 设置页（能力域 13/3/7/8）。
 * 三个 tab：
 *   - IM 平台状态（飞书/钉钉 Available 只读）
 *   - RBAC（角色 + 角色绑定 CRUD）
 *   - 通知配置（规则 / 模板 / 抑制规则 CRUD）
 */
import { useState } from "react";
import { Copy, KeyRound, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs } from "@/components/ui/tabs";
import {
  useAPIKeys,
  useAuditLogs,
  useCreateAPIKey,
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
/** IMTab 展示 IM 平台适配器可用性。注：凭证敏感，仅展示是否就绪，不回显。 */
function IMTab() {
  return (
    <div className="grid gap-3 md:grid-cols-2">
      <Card>
        <CardHeader><CardTitle className="text-base">飞书（Feishu）</CardTitle></CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          <p>配置环境变量 <code className="rounded bg-muted px-1">VIGIL_IM_FEISHU_APP_ID/APP_SECRET</code> 启用。</p>
          <p className="mt-2">能力：交互卡片✅ 卡片更新✅ 建群✅ @人✅ 命令机器人✅</p>
        </CardContent>
      </Card>
      <Card>
        <CardHeader><CardTitle className="text-base">钉钉（DingTalk）</CardTitle></CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          <p>配置 <code className="rounded bg-muted px-1">VIGIL_IM_DINGTALK_APP_KEY/APP_SECRET</code> 启用。</p>
          <p className="mt-2">能力：交互卡片✅ 卡片更新⚠️（降级发新消息）建群✅ @人✅ 命令机器人✅</p>
        </CardContent>
      </Card>
      <Card>
        <CardHeader><CardTitle className="text-base">企业微信（WeCom）</CardTitle></CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          <p>占位（NoopBot），待 PoC 后补真实适配器。</p>
        </CardContent>
      </Card>
    </div>
  );
}

// ===== RBAC =====
function RBACTab() {
  const roles = useRoles();
  const bindings = useRoleBindings();
  const delRole = useDeleteRole();
  const delBinding = useDeleteRoleBinding();

  return (
    <div className="grid gap-4 md:grid-cols-2">
      <Card>
        <CardHeader><CardTitle className="text-base">角色</CardTitle></CardHeader>
        <CardContent>
          {roles.isLoading ? (
            <Skeleton className="h-20 w-full" />
          ) : !roles.data || roles.data.length === 0 ? (
            <EmptyState title="无角色" />
          ) : (
            <div className="space-y-2">
              {roles.data.map((r) => (
                <div key={r.id} className="flex items-center justify-between rounded-md border p-2">
                  <div>
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium">{r.name}</span>
                      {r.builtin && <Badge variant="secondary" className="text-xs">内置</Badge>}
                      <Badge variant="outline" className="text-xs">{r.scope_level}</Badge>
                    </div>
                    <div className="mt-1 text-xs text-muted-foreground">
                      {r.permissions.length} 个权限点
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
        <CardHeader><CardTitle className="text-base">角色绑定（授权）</CardTitle></CardHeader>
        <CardContent>
          {bindings.isLoading ? (
            <Skeleton className="h-20 w-full" />
          ) : !bindings.data || bindings.data.length === 0 ? (
            <EmptyState title="无授权" description="给用户授予角色（含临时授权）。" />
          ) : (
            <div className="space-y-2">
              {bindings.data.map((b) => (
                <div key={b.id} className="flex items-center justify-between rounded-md border p-2 text-sm">
                  <div>
                    用户 #{b.user?.id ?? "?"} → 角色 #{b.role?.id ?? "?"}
                    {b.team_id && <span className="ml-2 text-xs text-muted-foreground">team #{b.team_id}</span>}
                    {b.expires_at && (
                      <Badge variant="outline" className="ml-2 text-xs">临时 {formatTime(b.expires_at)}</Badge>
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
    </div>
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
  return (
    <Card>
      <CardHeader><CardTitle className="text-base">通知规则</CardTitle></CardHeader>
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
    </Card>
  );
}

function SuppressionRulesSection() {
  const { data, isLoading } = useSuppressionRules();
  const del = useDeleteSuppressionRule();
  return (
    <Card>
      <CardHeader><CardTitle className="text-base">抑制规则（少打扰）</CardTitle></CardHeader>
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
    </Card>
  );
}

function TemplatesSection() {
  const { data, isLoading } = useNotificationTemplates();
  const del = useDeleteNotificationTemplate();
  return (
    <Card>
      <CardHeader><CardTitle className="text-base">通知模板</CardTitle></CardHeader>
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
    </Card>
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
      <div className="flex items-center gap-2">
        <p className="flex-1 text-sm text-muted-foreground">
          敏感操作留痕（角色变更/API Key/登录等）。只读。
        </p>
        <Select value={action} onChange={(e) => setAction(e.target.value)}>
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
