/**
 * 接入管理页（能力域 1）。
 * 列出告警源接入点，创建时返回 webhook URL + 一次性鉴权 token。
 * 仿 services.tsx 模式。
 */
import { useState } from "react";
import { Cable, Copy, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { useCreateIntegration, useDeleteIntegration, useIntegrations } from "@/hooks/integrations";
import { formatTime } from "@/lib/format";
import { toast } from "sonner";
import type { IntegrationType } from "@/lib/types";

const TYPE_OPTIONS: IntegrationType[] = ["prometheus", "grafana", "webhook", "email", "zabbix", "cloud", "api"];

export function Integrations() {
  const { data, isLoading } = useIntegrations();
  const del = useDeleteIntegration();
  const [creating, setCreating] = useState(false);

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-lg font-semibold">接入管理</h1>
          <p className="text-sm text-muted-foreground">告警源接入点 · webhook 鉴权 token</p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> 创建接入点
        </Button>
      </div>

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-32 w-full" />
          ) : !data || data.length === 0 ? (
            <EmptyState
              icon={<Cable className="h-8 w-8" />}
              title="暂无接入点"
              description="创建接入点后，告警源向 webhook URL 推送即可接入。"
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
                  <tr key={integ.id} className="border-b last:border-0">
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
                      <Button size="icon" variant="ghost" disabled={del.isPending} onClick={() => del.mutate(integ.id)}>
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

      {creating && <CreateIntegrationDialog onClose={() => setCreating(false)} />}
    </div>
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
