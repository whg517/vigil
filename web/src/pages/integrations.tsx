/**
 * 接入管理页（能力域 1）。
 * 列出告警源接入点，创建时返回 webhook URL + 一次性鉴权 token。
 * 仿 services.tsx 模式。
 */
import { useState } from "react";
import { Cable, Copy, Pencil, Plus, Trash2, Wand2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { useCreateIntegration, useDeleteIntegration, useIntegrations, useUpdateIntegration } from "@/hooks/integrations";
import { IntegrationWizard } from "@/pages/integration-wizard";
import { formatTime } from "@/lib/format";
import { toast } from "sonner";
import type { Integration, IntegrationType } from "@/lib/types";

const TYPE_OPTIONS: IntegrationType[] = ["prometheus", "grafana", "webhook", "email", "zabbix", "cloud", "api"];

export function Integrations() {
  const { data, isLoading } = useIntegrations();
  const [creating, setCreating] = useState(false);
  const [wizardOpen, setWizardOpen] = useState(false);
  const [editing, setEditing] = useState<Integration | undefined>(undefined);

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-lg font-semibold">接入管理</h1>
          <p className="text-sm text-muted-foreground">告警源接入点 · webhook 鉴权 token</p>
        </div>
        <div className="flex items-center gap-2">
          {/* 快速创建：保留原简单表单，供已熟悉配置的用户直接建点。 */}
          <Button variant="outline" onClick={() => setCreating(true)}>
            <Plus className="mr-1 h-4 w-4" /> 快速创建
          </Button>
          {/* 新建接入向导（M14.6）：分步引导选类型/配置/验证，降低 onboarding 门槛。 */}
          <Button onClick={() => setWizardOpen(true)}>
            <Wand2 className="mr-1 h-4 w-4" /> 新建接入向导
          </Button>
        </div>
      </div>

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-32 w-full" />
          ) : !data || data.length === 0 ? (
            <EmptyState
              icon={<Cable className="h-8 w-8" />}
              title="暂无接入点"
              description="用「新建接入向导」分步接入告警源，或点「快速创建」直接建点。"
              action={
                <Button onClick={() => setWizardOpen(true)}>
                  <Wand2 className="mr-1 h-4 w-4" /> 新建接入向导
                </Button>
              }
            />
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">名称</th>
                  <th className="p-3">类型</th>
                  <th className="p-3">状态</th>
                  <th className="p-3">创建时间</th>
                  <th className="p-3"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((integ) => (
                  <IntegrationRow key={integ.id} integ={integ} onEdit={() => setEditing(integ)} />
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>

      {creating && <CreateIntegrationDialog onClose={() => setCreating(false)} />}
      {wizardOpen && <IntegrationWizard onClose={() => setWizardOpen(false)} />}
      {editing && <EditIntegrationDialog integ={editing} onClose={() => setEditing(undefined)} />}
    </div>
  );
}

/** IntegrationRow 单行 + 启停/编辑/删除。 */
function IntegrationRow({ integ, onEdit }: { integ: Integration; onEdit: () => void }) {
  const del = useDeleteIntegration();
  const update = useUpdateIntegration();
  return (
    <tr className="border-b last:border-0">
      <td className="p-3 font-medium">{integ.name}</td>
      <td className="p-3">
        <Badge variant="secondary">{integ.type}</Badge>
      </td>
      <td className="p-3">
        <Badge variant={integ.enabled ? "default" : "secondary"}>
          {integ.enabled ? "启用" : "停用"}
        </Badge>
      </td>
      <td className="p-3 text-muted-foreground">{formatTime(integ.created_at)}</td>
      <td className="p-3 text-right">
        <div className="flex items-center justify-end gap-1">
          <Button
            variant="ghost"
            size="sm"
            title={integ.enabled ? "停用" : "启用"}
            disabled={update.isPending}
            onClick={() => update.mutate({ id: integ.id, body: { enabled: !integ.enabled } })}
          >
            {integ.enabled ? "停用" : "启用"}
          </Button>
          <Button size="icon" variant="ghost" title="编辑" onClick={onEdit}>
            <Pencil className="h-4 w-4" />
          </Button>
          <Button size="icon" variant="ghost" title="删除" disabled={del.isPending} onClick={() => del.mutate(integ.id)}>
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </td>
    </tr>
  );
}

/** EditIntegrationDialog 改名 + 启停（type 创建后不可改）。 */
function EditIntegrationDialog({ integ, onClose }: { integ: Integration; onClose: () => void }) {
  const update = useUpdateIntegration();
  const [name, setName] = useState(integ.name);
  const [enabled, setEnabled] = useState(!!integ.enabled);

  return (
    <Dialog open onClose={onClose} title={`编辑接入点 · ${integ.type}`} description="类型创建后不可修改。">
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          update.mutate({ id: integ.id, body: { name, enabled } }, { onSuccess: onClose });
        }}
      >
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            className="h-4 w-4"
          />
          <span>启用（停用后告警源推送将被拒绝）</span>
        </label>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>取消</Button>
          <Button type="submit" disabled={update.isPending || !name}>保存</Button>
        </div>
      </form>
    </Dialog>
  );
}

/** CreateIntegrationDialog 创建接入点。成功后展示 webhook URL + 一次性 token。 */
function CreateIntegrationDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateIntegration();
  const [name, setName] = useState("");
  const [type, setType] = useState<IntegrationType>("prometheus");
  const [created, setCreated] = useState<{ token: string; webhookUrl: string } | null>(null);

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate({ name, type }, { onSuccess: (data) => {
      const base = window.location.origin;
      setCreated({ token: data.token, webhookUrl: `${base}/api/v1/webhook/${data.token}` });
    }});
  };

  const copy = async (text: string) => {
    try { await navigator.clipboard.writeText(text); toast.success("已复制"); }
    catch { toast.error("复制失败"); }
  };

  // 创建成功：展示 webhook URL + 一次性 token
  if (created) {
    return (
      <Dialog open onClose={onClose} title="接入点已创建" description="⚠️ 鉴权 token 仅此一次展示，请立即复制保存。">
        <div className="space-y-3">
          <div>
            <label className="text-sm font-medium">Webhook URL（告警源推送到此）</label>
            <div className="mt-1 flex items-center gap-2 rounded-md border bg-muted p-3">
              <code className="flex-1 break-all text-xs">{created.webhookUrl}</code>
              <Button size="sm" variant="outline" onClick={() => copy(created.webhookUrl)}>
                <Copy className="mr-1 h-4 w-4" /> 复制
              </Button>
            </div>
          </div>
          <div>
            <label className="text-sm font-medium">鉴权 Token（如需 Header 方式）</label>
            <div className="mt-1 flex items-center gap-2 rounded-md border bg-muted p-3">
              <code className="flex-1 break-all text-xs">{created.token}</code>
              <Button size="sm" variant="outline" onClick={() => copy(created.token)}>
                <Copy className="mr-1 h-4 w-4" /> 复制
              </Button>
            </div>
          </div>
          <Button className="w-full" onClick={onClose}>我已保存</Button>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog open onClose={onClose} title="创建接入点" description="配置告警源接入。创建后获得 webhook URL。">
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="prod-prometheus" required autoFocus />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">类型</label>
          <Select value={type} onChange={(e) => setType(e.target.value as IntegrationType)}>
            {TYPE_OPTIONS.map((t) => <option key={t} value={t}>{t}</option>)}
          </Select>
        </div>
        <Button type="submit" className="w-full" disabled={create.isPending || !name}>
          {create.isPending ? "创建中..." : "创建"}
        </Button>
      </form>
    </Dialog>
  );
}
