/**
 * 订阅（能力域 4/7 T4.4）—— SubscriptionTab：当前登录用户管理自己的定向订阅。
 * 列出我的订阅（scope=team/service + min_severity + channels）+ 新建 + 删除。
 * 后端：GET/POST/DELETE /subscriptions（均作用于自己的订阅）。
 */
import { useState } from "react";
import { BellRing, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useCreateSubscription,
  useDeleteSubscription,
  useSubscriptions,
} from "@/hooks/subscriptions";
import { useTeams } from "@/hooks/users-teams";
import { useServices } from "@/hooks/services";
import type { Subscription, SubscriptionSeverity } from "@/lib/types";

const SEVERITY_OPTIONS: SubscriptionSeverity[] = ["critical", "warning", "info"];
const CHANNELS = ["im", "email", "sms", "phone", "webhook"] as const;

/** scopeLabel 从订阅的 team/service edge 提取可读 scope 描述。 */
function scopeLabel(sub: Subscription): string {
  if (sub.team) return `团队 · ${String(sub.team.name ?? `#${sub.team.id}`)}`;
  if (sub.service) return `服务 · ${String(sub.service.name ?? `#${sub.service.id}`)}`;
  return "—";
}

/** SubscriptionTab：当前用户的个人订阅 CRUD。 */
export function SubscriptionTab() {
  const { data, isLoading } = useSubscriptions();
  const del = useDeleteSubscription();
  const [creating, setCreating] = useState(false);

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">
          定向订阅你关注的团队 / 服务的事件通知（这是你个人的订阅，仅对你自己生效）。
        </p>
        <Button size="sm" onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> 新建订阅
        </Button>
      </div>
      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-20 w-full" />
          ) : !data || data.length === 0 ? (
            <EmptyState
              icon={<BellRing className="h-8 w-8" />}
              title="暂无订阅"
              description="订阅你关注的团队或服务，达到严重度阈值的事件将通知你。"
            />
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">订阅范围</th>
                  <th className="p-3">最低严重度</th>
                  <th className="p-3">通道</th>
                  <th className="p-3"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((sub) => (
                  <tr key={sub.id} className="border-b last:border-0">
                    <td className="p-3 font-medium">{scopeLabel(sub)}</td>
                    <td className="p-3">
                      <Badge variant="secondary">{sub.min_severity}</Badge>
                    </td>
                    <td className="p-3">
                      {sub.channels && sub.channels.length > 0 ? (
                        <div className="flex flex-wrap gap-1">
                          {sub.channels.map((ch) => (
                            <Badge key={ch} variant="outline" className="text-xs">{ch}</Badge>
                          ))}
                        </div>
                      ) : (
                        <span className="text-xs text-muted-foreground">默认链</span>
                      )}
                    </td>
                    <td className="p-3 text-right">
                      <Button
                        size="icon"
                        variant="ghost"
                        title="删除"
                        disabled={del.isPending}
                        onClick={() => del.mutate(sub.id)}
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
      {creating && <CreateSubscriptionDialog onClose={() => setCreating(false)} />}
    </div>
  );
}

/** CreateSubscriptionDialog 新建订阅：选 scope（team 或 service）+ 最低严重度 + 通道（多选）。 */
function CreateSubscriptionDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateSubscription();
  const { data: teams } = useTeams();
  const { data: services } = useServices();
  const [scopeKind, setScopeKind] = useState<"team" | "service">("team");
  const [teamId, setTeamId] = useState<number | undefined>(undefined);
  const [serviceId, setServiceId] = useState<number | undefined>(undefined);
  const [minSeverity, setMinSeverity] = useState<SubscriptionSeverity>("warning");
  const [channels, setChannels] = useState<string[]>([]);

  const toggleChannel = (ch: string) =>
    setChannels((prev) => (prev.includes(ch) ? prev.filter((c) => c !== ch) : [...prev, ch]));

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    // scope 二选一：team 或 service（后端要求恰好一个）。
    const body =
      scopeKind === "team"
        ? { team_id: teamId, min_severity: minSeverity, channels: channels.length ? channels : undefined }
        : { service_id: serviceId, min_severity: minSeverity, channels: channels.length ? channels : undefined };
    create.mutate(body, { onSuccess: onClose });
  };

  const scopeValid = scopeKind === "team" ? !!teamId : !!serviceId;

  return (
    <Dialog
      open
      onClose={onClose}
      title="新建订阅"
      description="订阅一个团队或服务的事件通知（仅能订阅你有权查看的范围）。"
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">订阅类型</label>
            <Select
              value={scopeKind}
              onChange={(e) => setScopeKind(e.target.value as "team" | "service")}
            >
              <option value="team">团队</option>
              <option value="service">服务</option>
            </Select>
          </div>
          {scopeKind === "team" ? (
            <div className="space-y-1.5">
              <label className="text-sm font-medium">团队</label>
              <Select
                value={teamId ? String(teamId) : ""}
                onChange={(e) => setTeamId(e.target.value ? Number(e.target.value) : undefined)}
              >
                <option value="">选择团队…</option>
                {teams?.map((t) => (
                  <option key={t.id} value={t.id}>{t.name}</option>
                ))}
              </Select>
            </div>
          ) : (
            <div className="space-y-1.5">
              <label className="text-sm font-medium">服务</label>
              <Select
                value={serviceId ? String(serviceId) : ""}
                onChange={(e) => setServiceId(e.target.value ? Number(e.target.value) : undefined)}
              >
                <option value="">选择服务…</option>
                {services?.map((s) => (
                  <option key={s.id} value={s.id}>{s.name}</option>
                ))}
              </Select>
            </div>
          )}
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">最低严重度（达到即通知）</label>
          <Select
            value={minSeverity}
            onChange={(e) => setMinSeverity(e.target.value as SubscriptionSeverity)}
          >
            {SEVERITY_OPTIONS.map((s) => (
              <option key={s} value={s}>{s}</option>
            ))}
          </Select>
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">
            通道偏好 <span className="text-xs text-muted-foreground">（留空=默认链）</span>
          </label>
          <div className="flex flex-wrap gap-1">
            {CHANNELS.map((ch) => {
              const on = channels.includes(ch);
              return (
                <button
                  key={ch}
                  type="button"
                  onClick={() => toggleChannel(ch)}
                  className={`rounded-md border px-2 py-0.5 text-xs transition-colors ${
                    on ? "border-primary bg-primary text-primary-foreground" : "hover:bg-accent"
                  }`}
                >
                  {ch}
                </button>
              );
            })}
          </div>
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>取消</Button>
          <Button type="submit" disabled={create.isPending || !scopeValid}>
            {create.isPending ? "创建中..." : "订阅"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
