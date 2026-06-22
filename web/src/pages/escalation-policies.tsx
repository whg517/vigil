/**
 * 升级策略管理页（能力域 6）。
 * 列出升级策略，创建时配置 name + levels（层级/延迟/通道）。
 */
import { useState } from "react";
import { ChevronUp, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { useCreateEscalationPolicy, useDeleteEscalationPolicy, useEscalationPolicies } from "@/hooks/escalation-policies";
import { formatTime } from "@/lib/format";
import type { EscalationLevel } from "@/lib/types";

export function EscalationPolicies() {
  const { data, isLoading } = useEscalationPolicies();
  const del = useDeleteEscalationPolicy();
  const [creating, setCreating] = useState(false);

  const levelSummary = (levels?: EscalationLevel[]) => {
    if (!levels || levels.length === 0) return "—";
    return levels.map((l) => `L${l.level}·${l.delay_minutes}min`).join(" → ");
  };

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-lg font-semibold">升级策略</h1>
          <p className="text-sm text-muted-foreground">未 ack 时按层级升级通知</p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> 创建策略
        </Button>
      </div>

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-32 w-full" />
          ) : !data || data.length === 0 ? (
            <EmptyState
              icon={<ChevronUp className="h-8 w-8" />}
              title="暂无升级策略"
              description="创建升级策略，配置未响应时的升级层级与延迟。"
            />
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">名称</th>
                  <th className="p-3">层级</th>
                  <th className="p-3">重复</th>
                  <th className="p-3">创建时间</th>
                  <th className="p-3"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((p) => (
                  <tr key={p.id} className="border-b last:border-0">
                    <td className="p-3 font-medium">{p.name}</td>
                    <td className="p-3 text-muted-foreground">{levelSummary(p.levels)}</td>
                    <td className="p-3"><Badge variant="secondary">{p.repeat_times}</Badge></td>
                    <td className="p-3 text-muted-foreground">{formatTime(p.created_at)}</td>
                    <td className="p-3 text-right">
                      <Button size="icon" variant="ghost" disabled={del.isPending} onClick={() => del.mutate(p.id)}>
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

      {creating && <CreateEscalationPolicyDialog onClose={() => setCreating(false)} />}
    </div>
  );
}

/** CreateEscalationPolicyDialog 创建升级策略。简化：name + 单层级配置。 */
function CreateEscalationPolicyDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateEscalationPolicy();
  const [name, setName] = useState("");
  const [delayMinutes, setDelayMinutes] = useState("5");
  const [channel, setChannel] = useState<"im" | "phone" | "sms" | "email" | "webhook">("im");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    // 简化版：创建一个单层升级策略（L1，配置延迟和通道）
    const levels: EscalationLevel[] = [{
      level: 1,
      delay_minutes: parseInt(delayMinutes, 10) || 5,
      targets: [],
      notify_channels: [channel],
    }];
    create.mutate({ name, levels }, { onSuccess: onClose });
  };

  return (
    <Dialog open onClose={onClose} title="创建升级策略" description="配置未响应时的升级层级（简化版：单层，创建后可编辑细化）。">
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="默认升级（5min→IM）" required autoFocus />
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">L1 延迟（分钟）</label>
            <Input type="number" min={1} value={delayMinutes} onChange={(e) => setDelayMinutes(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">L1 通道</label>
            <Select value={channel} onChange={(e) => setChannel(e.target.value as "im" | "phone" | "sms" | "email" | "webhook")}>
              <option value="im">im</option>
              <option value="phone">phone</option>
              <option value="sms">sms</option>
              <option value="email">email</option>
              <option value="webhook">webhook</option>
            </Select>
          </div>
        </div>
        <Button type="submit" className="w-full" disabled={create.isPending || !name}>
          {create.isPending ? "创建中..." : "创建"}
        </Button>
      </form>
    </Dialog>
  );
}
