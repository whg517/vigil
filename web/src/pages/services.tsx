/**
 * Services —— 服务目录页（能力域 4/13）。
 * 列表（label/status）+ 创建表单 + 删除。
 * 后端：GET/POST/PATCH/DELETE /services。
 */
import * as React from "react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Pencil, Plus, Trash2, Boxes } from "lucide-react";
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
  useUpdateService,
} from "@/hooks/services";
import { formatTime } from "@/lib/format";
import type { Service } from "@/lib/types";

export function Services() {
  const { t } = useTranslation();
  const { data, isLoading, isError } = useServices();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Service | undefined>(undefined);

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("services.title")}</h1>
          <p className="text-sm text-muted-foreground">{t("services.subtitle")}</p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> {t("services.create")}
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
              title={t("services.emptyTitle")}
              description={t("services.emptyDescription")}
            />
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 text-left font-medium">{t("services.colName")}</th>
                <th className="px-4 py-2.5 text-left font-medium">{t("services.colSlug")}</th>
                <th className="px-4 py-2.5 text-left font-medium">{t("services.colLabels")}</th>
                <th className="px-4 py-2.5 text-left font-medium">{t("services.colStatus")}</th>
                <th className="px-4 py-2.5 text-left font-medium">{t("services.colAutoCreate")}</th>
                <th className="px-4 py-2.5 text-left font-medium">{t("services.colCreatedAt")}</th>
                <th className="px-4 py-2.5"></th>
              </tr>
            </thead>
            <tbody>
              {data.map((s) => (
                <ServiceRow key={s.id} svc={s} onEdit={() => setEditing(s)} />
              ))}
            </tbody>
          </table>
        )}
      </Card>

      {creating && <CreateServiceDialog onClose={() => setCreating(false)} />}
      {editing && <EditServiceDialog svc={editing} onClose={() => setEditing(undefined)} />}
    </div>
  );
}

/** ServiceRow 单行 + 编辑/删除。 */
function ServiceRow({ svc, onEdit }: { svc: Service; onEdit: () => void }) {
  const { t } = useTranslation();
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
          {svc.status === "active" ? t("services.statusActive") : t("services.statusDisabled")}
        </Badge>
      </td>
      <td className="px-4 py-3 text-xs text-muted-foreground">
        {svc.auto_create_incident ? t("services.yes") : t("services.no")}
      </td>
      <td className="px-4 py-3 text-xs text-muted-foreground">{formatTime(svc.created_at)}</td>
      <td className="px-4 py-3 text-right">
        <div className="flex items-center justify-end gap-1">
          <Button variant="ghost" size="icon" title={t("common.edit")} onClick={onEdit}>
            <Pencil className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            title={t("common.delete")}
            onClick={() => del.mutate(svc.id)}
            disabled={del.isPending}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </td>
    </tr>
  );
}

/** CreateServiceDialog 创建服务表单。 */
function CreateServiceDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
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
    <Dialog open onClose={onClose} title={t("services.createTitle")} description={t("services.createDescription")}>
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
        <Field label={t("services.fieldName")}>
          <Input value={name} onChange={(e) => setName(e.target.value)} required placeholder="payment-api" />
        </Field>
        <Field label={t("services.fieldSlug")}>
          <Input value={slug} onChange={(e) => setSlug(e.target.value)} required placeholder="payment" />
        </Field>
        <Field label={t("services.fieldLabels")}>
          <Input value={labelsText} onChange={(e) => setLabelsText(e.target.value)} placeholder="env=prod,tier=1" />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label={t("services.fieldStatus")}>
            <Select value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="active">{t("services.statusActive")}</option>
              <option value="disabled">{t("services.statusDisabled")}</option>
            </Select>
          </Field>
          <Field label={t("services.fieldAutoCreate")}>
            <Select
              value={autoCreate ? "true" : "false"}
              onChange={(e) => setAutoCreate(e.target.value === "true")}
            >
              <option value="true">{t("services.yes")}</option>
              <option value="false">{t("services.no")}</option>
            </Select>
          </Field>
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" disabled={create.isPending || !name || !slug}>
            {t("common.create")}
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

/** EditServiceDialog 编辑服务（名称/标签/状态/自动建事件；slug 创建后不可改）。 */
function EditServiceDialog({ svc, onClose }: { svc: Service; onClose: () => void }) {
  const { t } = useTranslation();
  const update = useUpdateService(svc.id);
  const [name, setName] = useState(svc.name);
  const [status, setStatus] = useState(svc.status);
  const [autoCreate, setAutoCreate] = useState(!!svc.auto_create_incident);
  // 标签以 "k=v,k=v" 文本编辑，回填现有值
  const [labelsText, setLabelsText] = useState(
    Object.entries(svc.labels ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join(","),
  );

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
    <Dialog
      open
      onClose={onClose}
      title={t("services.editTitle", { slug: svc.slug })}
      description={t("services.editDescription")}
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          update.mutate(
            { name, status: status as Service["status"], auto_create_incident: autoCreate, labels },
            { onSuccess: onClose },
          );
        }}
      >
        <Field label={t("services.fieldName")}>
          <Input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        </Field>
        <Field label={t("services.fieldLabels")}>
          <Input value={labelsText} onChange={(e) => setLabelsText(e.target.value)} placeholder="env=prod,tier=1" />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label={t("services.fieldStatus")}>
            <Select value={status} onChange={(e) => setStatus(e.target.value as Service["status"])}>
              <option value="active">{t("services.statusActive")}</option>
              <option value="disabled">{t("services.statusDisabled")}</option>
            </Select>
          </Field>
          <Field label={t("services.fieldAutoCreate")}>
            <Select
              value={autoCreate ? "true" : "false"}
              onChange={(e) => setAutoCreate(e.target.value === "true")}
            >
              <option value="true">{t("services.yes")}</option>
              <option value="false">{t("services.no")}</option>
            </Select>
          </Field>
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" disabled={update.isPending || !name}>
            {t("common.save")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
