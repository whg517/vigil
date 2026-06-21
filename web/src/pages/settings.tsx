/**
 * Settings —— 设置页（能力域 13/3/7/8）。
 * 三个 tab：
 *   - IM 平台状态（飞书/钉钉 Available 只读）
 *   - RBAC（角色 + 角色绑定 CRUD）
 *   - 通知配置（规则 / 模板 / 抑制规则 CRUD）
 */
import { useState } from "react";
import { Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs } from "@/components/ui/tabs";
import {
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
          { value: "notification", label: "通知配置" },
          { value: "im", label: "IM 平台" },
        ]}
      />
      {tab === "rbac" && <RBACTab />}
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
                    用户 #{b.user_id} → 角色 #{b.role_id}
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
