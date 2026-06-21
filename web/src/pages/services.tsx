/**
 * Services —— 服务目录页（能力域 4/13）。
 * 列表（label/status）+ 创建表单 + 删除。
 * 后端：GET/POST/PATCH/DELETE /services。
 */
import * as React from "react";
import { useState } from "react";
import { Plus, Trash2, Boxes } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useCreateService,
  useDeleteService,
  useServices,
} from "@/hooks/services";
import { formatTime } from "@/lib/format";
import type { Service } from "@/lib/types";

export function Services() {
  const { data, isLoading, isError } = useServices();
  const [creating, setCreating] = useState(false);

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">服务</h1>
          <p className="text-sm text-muted-foreground">
            服务是路由的锚点与软隔离的载体，告警的 label 匹配 Service 命中路由。
          </p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> 创建服务
        </Button>
      </div>

      <Card className="overflow-hidden">
        {isLoading ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 5 }).map((_, i) => (
              <Skeleton key={i} className="h-10 w-full" />
            ))}
          </div>
        ) : isError ? null : !data || data.length === 0 ? (
          <div className="p-6">
            <EmptyState
              icon={<Boxes className="h-8 w-8" />}
              title="还没有服务"
              description="创建第一个服务，告警将按 label 路由到对应服务。"
            />
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 text-left font-medium">名称</th>
                <th className="px-4 py-2.5 text-left font-medium">Slug</th>
                <th className="px-4 py-2.5 text-left font-medium">标签</th>
                <th className="px-4 py-2.5 text-left font-medium">状态</th>
                <th className="px-4 py-2.5 text-left font-medium">自动建事件</th>
                <th className="px-4 py-2.5 text-left font-medium">创建时间</th>
                <th className="px-4 py-2.5"></th>
              </tr>
            </thead>
            <tbody>
              {data.map((s) => (
                <ServiceRow key={s.id} svc={s} />
              ))}
            </tbody>
          </table>
        )}
      </Card>

      {creating && <CreateServiceDialog onClose={() => setCreating(false)} />}
    </div>
  );
}

/** ServiceRow 单行 + 删除。 */
function ServiceRow({ svc }: { svc: Service }) {
  const del = useDeleteService();
  return (
    <tr className="border-b last:border-0 hover:bg-muted/30">
      <td className="px-4 py-3 font-medium">{svc.name}</td>
      <td className="px-4 py-3 font-mono text-xs text-muted-foreground">{svc.slug}</td>
      <td className="px-4 py-3">
        {svc.labels && Object.keys(svc.labels).length > 0 ? (
          <div className="flex flex-wrap gap-1">
            {Object.entries(svc.labels).map(([k, v]) => (
              <Badge key={k} variant="outline" className="font-mono text-xs">
                {k}={v}
              </Badge>
            ))}
          </div>
        ) : (
          <span className="text-xs text-muted-foreground">—</span>
        )}
      </td>
      <td className="px-4 py-3">
        <Badge variant={svc.status === "active" ? "default" : "secondary"}>
          {svc.status === "active" ? "启用" : "停用"}
        </Badge>
      </td>
      <td className="px-4 py-3 text-xs text-muted-foreground">
        {svc.auto_create_incident ? "是" : "否"}
      </td>
      <td className="px-4 py-3 text-xs text-muted-foreground">{formatTime(svc.created_at)}</td>
      <td className="px-4 py-3 text-right">
        <Button
          variant="ghost"
          size="icon"
          onClick={() => del.mutate(svc.id)}
          disabled={del.isPending}
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      </td>
    </tr>
  );
}

/** CreateServiceDialog 创建服务表单。 */
function CreateServiceDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateService();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [status, setStatus] = useState("active");
  const [autoCreate, setAutoCreate] = useState(true);
  const [labelsText, setLabelsText] = useState(""); // env=prod,tier=1

  const labels = labelsText
    .split(",")
    .map((kv) => kv.trim())
    .filter(Boolean)
    .reduce<Record<string, string>>((acc, kv) => {
      const [k, v] = kv.split("=");
      if (k && v) acc[k.trim()] = v.trim();
      return acc;
    }, {});

  return (
    <Dialog open onClose={onClose} title="创建服务" description="服务的 label 用于告警路由匹配。">
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate(
            { name, slug, status: status as Service["status"], auto_create_incident: autoCreate, labels },
            { onSuccess: onClose },
          );
        }}
      >
        <Field label="名称">
          <Input value={name} onChange={(e) => setName(e.target.value)} required placeholder="payment-api" />
        </Field>
        <Field label="Slug（唯一标识）">
          <Input value={slug} onChange={(e) => setSlug(e.target.value)} required placeholder="payment" />
        </Field>
        <Field label="标签（逗号分隔 key=value）">
          <Input value={labelsText} onChange={(e) => setLabelsText(e.target.value)} placeholder="env=prod,tier=1" />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="状态">
            <Select value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="active">启用</option>
              <option value="disabled">停用</option>
            </Select>
          </Field>
          <Field label="自动建事件">
            <Select
              value={autoCreate ? "true" : "false"}
              onChange={(e) => setAutoCreate(e.target.value === "true")}
            >
              <option value="true">是</option>
              <option value="false">否</option>
            </Select>
          </Field>
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" disabled={create.isPending || !name || !slug}>
            创建
          </Button>
        </div>
      </form>
    </Dialog>
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
