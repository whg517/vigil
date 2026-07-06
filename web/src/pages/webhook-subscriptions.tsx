/**
 * 出站 webhook 订阅页（能力域 1，N2.2）。
 * 列出出站订阅（把 incident 变更推送到外部 URL），支持新建/编辑/启停/删除。
 * 后端：GET/POST/PATCH/DELETE /webhook-subscriptions。
 * signing_secret 经 Sensitive 恒不回显；创建/编辑时填明文加密落库，编辑留空=不改动。
 * 仿 integrations.tsx 模式。
 */
import { useState } from "react";
import { Pencil, Plus, Trash2, Webhook } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import {
  useCreateWebhookSubscription,
  useDeleteWebhookSubscription,
  useUpdateWebhookSubscription,
  useWebhookSubscriptions,
} from "@/hooks/webhook-subscriptions";
import { formatTime } from "@/lib/format";
import type { WebhookSubscription } from "@/lib/types";

// EVENT_TYPES 可订阅的事件类型（对应后端 incident.<action> 领域事件，见 webhook/dispatcher.go）。
// 空选择 = 订阅所有事件（后端不过滤）。列表为预设，未来新增类型仍可手工在编辑时补齐。
const EVENT_TYPES = [
  "incident.ack",
  "incident.resolve",
  "incident.escalate",
  "incident.close",
  "incident.reopen",
  "incident.add_responder",
  "incident.merge",
];

export function WebhookSubscriptions() {
  const { data, isLoading } = useWebhookSubscriptions();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<WebhookSubscription | undefined>(undefined);

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">出站订阅</h1>
          <p className="text-sm text-muted-foreground">
            把事件（incident）变更推送到外部 URL。可按事件类型过滤、配置签名密钥防伪。
          </p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> 创建订阅
        </Button>
      </div>

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <div className="p-4">
              <EmptyState icon={<Webhook className="h-8 w-8" />} title="加载中…" />
            </div>
          ) : !data || data.length === 0 ? (
            <div className="p-6">
              <EmptyState
                icon={<Webhook className="h-8 w-8" />}
                title="暂无出站订阅"
                description="创建订阅后，事件变更会推送到你的外部 URL。"
              />
            </div>
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
                <tr>
                  <th className="px-4 py-2.5 text-left font-medium">名称</th>
                  <th className="px-4 py-2.5 text-left font-medium">URL</th>
                  <th className="px-4 py-2.5 text-left font-medium">事件类型</th>
                  <th className="px-4 py-2.5 text-left font-medium">状态</th>
                  <th className="px-4 py-2.5 text-left font-medium">创建时间</th>
                  <th className="px-4 py-2.5"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((sub) => (
                  <SubscriptionRow key={sub.id} sub={sub} onEdit={() => setEditing(sub)} />
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>

      {creating && <CreateSubscriptionDialog onClose={() => setCreating(false)} />}
      {editing && (
        <EditSubscriptionDialog sub={editing} onClose={() => setEditing(undefined)} />
      )}
    </div>
  );
}

/** SubscriptionRow 单行 + 启停/编辑/删除。 */
function SubscriptionRow({
  sub,
  onEdit,
}: {
  sub: WebhookSubscription;
  onEdit: () => void;
}) {
  const del = useDeleteWebhookSubscription();
  const update = useUpdateWebhookSubscription();
  const types = sub.event_types ?? [];
  return (
    <tr className="border-b last:border-0 hover:bg-muted/30">
      <td className="px-4 py-3 font-medium">{sub.name || "—"}</td>
      <td className="px-4 py-3 font-mono text-xs text-muted-foreground">
        <span className="block max-w-xs truncate" title={sub.url}>
          {sub.url}
        </span>
      </td>
      <td className="px-4 py-3">
        {types.length === 0 ? (
          <Badge variant="outline" className="text-xs">
            所有事件
          </Badge>
        ) : (
          <div className="flex flex-wrap gap-1">
            {types.map((t) => (
              <Badge key={t} variant="secondary" className="font-mono text-xs">
                {t}
              </Badge>
            ))}
          </div>
        )}
      </td>
      <td className="px-4 py-3">
        <Badge variant={sub.enabled ? "default" : "secondary"}>
          {sub.enabled ? "启用" : "停用"}
        </Badge>
      </td>
      <td className="px-4 py-3 text-xs text-muted-foreground">{formatTime(sub.created_at)}</td>
      <td className="px-4 py-3 text-right">
        <div className="flex items-center justify-end gap-1">
          <Button
            variant="ghost"
            size="sm"
            title={sub.enabled ? "停用" : "启用"}
            disabled={update.isPending}
            onClick={() => update.mutate({ id: sub.id, body: { enabled: !sub.enabled } })}
          >
            {sub.enabled ? "停用" : "启用"}
          </Button>
          <Button variant="ghost" size="icon" title="编辑" onClick={onEdit}>
            <Pencil className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            title="删除"
            disabled={del.isPending}
            onClick={() => del.mutate(sub.id)}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </td>
    </tr>
  );
}

/** EventTypeSelector 事件类型多选（预设按钮 toggle）。空选择=订阅所有事件。 */
function EventTypeSelector({
  selected,
  onToggle,
}: {
  selected: string[];
  onToggle: (t: string) => void;
}) {
  return (
    <div className="space-y-1.5">
      <label className="text-sm font-medium">事件类型（多选，留空=订阅所有）</label>
      <div className="flex flex-wrap gap-2">
        {EVENT_TYPES.map((t) => (
          <button
            key={t}
            type="button"
            onClick={() => onToggle(t)}
            className={`rounded-md border px-3 py-1 font-mono text-xs transition-colors ${
              selected.includes(t)
                ? "border-primary bg-primary text-primary-foreground"
                : "hover:bg-accent"
            }`}
          >
            {t}
          </button>
        ))}
      </div>
    </div>
  );
}

/** CreateSubscriptionDialog 创建订阅。signing_secret 为明文，加密落库后不再回显。 */
function CreateSubscriptionDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateWebhookSubscription();
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [eventTypes, setEventTypes] = useState<string[]>([]);
  const [signingSecret, setSigningSecret] = useState("");
  const [enabled, setEnabled] = useState(true);

  const toggleType = (t: string) =>
    setEventTypes((prev) => (prev.includes(t) ? prev.filter((x) => x !== t) : [...prev, t]));

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate(
      {
        name: name || undefined,
        url,
        event_types: eventTypes,
        signing_secret: signingSecret || undefined,
        enabled,
      },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title="创建出站订阅"
      description="事件变更将推送到此 URL。签名密钥用于对方校验来源（可选）。"
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称（管理识别用，可选）</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="外部告警中心" />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">出站 URL</label>
          <Input
            type="url"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://example.com/webhook"
            required
            autoFocus
          />
        </div>
        <EventTypeSelector selected={eventTypes} onToggle={toggleType} />
        <div className="space-y-1.5">
          <label className="text-sm font-medium">签名密钥（可选，加密存储、不回显）</label>
          <Input
            type="password"
            value={signingSecret}
            onChange={(e) => setSigningSecret(e.target.value)}
            placeholder="留空=不签名"
            autoComplete="new-password"
          />
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            className="h-4 w-4"
          />
          <span>启用（停用后不推送）</span>
        </label>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" disabled={create.isPending || !url}>
            {create.isPending ? "创建中..." : "创建"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** EditSubscriptionDialog 编辑订阅。签名密钥留空=不改；填空白清空签名靠后端语义（此处只在有输入时提交）。 */
function EditSubscriptionDialog({
  sub,
  onClose,
}: {
  sub: WebhookSubscription;
  onClose: () => void;
}) {
  const update = useUpdateWebhookSubscription();
  const [name, setName] = useState(sub.name ?? "");
  const [url, setUrl] = useState(sub.url);
  const [eventTypes, setEventTypes] = useState<string[]>(sub.event_types ?? []);
  const [signingSecret, setSigningSecret] = useState("");
  const [rotateSecret, setRotateSecret] = useState(false);
  const [enabled, setEnabled] = useState(!!sub.enabled);

  const toggleType = (t: string) =>
    setEventTypes((prev) => (prev.includes(t) ? prev.filter((x) => x !== t) : [...prev, t]));

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    // signing_secret 仅在勾选"更新密钥"时提交（空串=清空签名，明文=重新加密）。
    const body: Parameters<typeof update.mutate>[0]["body"] = {
      name,
      url,
      event_types: eventTypes,
      enabled,
    };
    if (rotateSecret) {
      body.signing_secret = signingSecret;
    }
    update.mutate({ id: sub.id, body }, { onSuccess: onClose });
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title="编辑出站订阅"
      description="签名密钥不回显；如需更换或清空，勾选下方选项。"
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称（管理识别用，可选）</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="外部告警中心" />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">出站 URL</label>
          <Input
            type="url"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            required
            autoFocus
          />
        </div>
        <EventTypeSelector selected={eventTypes} onToggle={toggleType} />
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={rotateSecret}
            onChange={(e) => setRotateSecret(e.target.checked)}
            className="h-4 w-4"
          />
          <span>更新签名密钥（不勾选=保持不变）</span>
        </label>
        {rotateSecret && (
          <div className="space-y-1.5">
            <label className="text-sm font-medium">新签名密钥（留空=清空签名）</label>
            <Input
              type="password"
              value={signingSecret}
              onChange={(e) => setSigningSecret(e.target.value)}
              placeholder="留空=停用签名"
              autoComplete="new-password"
            />
          </div>
        )}
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            className="h-4 w-4"
          />
          <span>启用（停用后不推送）</span>
        </label>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" disabled={update.isPending || !url}>
            {update.isPending ? "保存中..." : "保存"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
