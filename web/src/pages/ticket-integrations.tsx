/**
 * 工单集成配置页（能力域 4 T4.3）。
 * 出向工单集成（type/endpoint/credential/config/归属）：list/create/update/delete。
 * 后端：GET/POST/PATCH/DELETE /ticket-integrations（凭据经 Sensitive 不回显）。
 * 仿 integrations.tsx / escalation-policies.tsx 模式。
 */
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Pencil, Plus, Ticket, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import {
  useCreateTicketIntegration,
  useDeleteTicketIntegration,
  useTicketIntegrations,
  useUpdateTicketIntegration,
} from "@/hooks/ticket-integrations";
import { useTeams } from "@/hooks/users-teams";
import { formatTime } from "@/lib/format";
import { toast } from "sonner";
import type { TicketIntegration, TicketIntegrationType } from "@/lib/types";

const TYPE_OPTIONS: TicketIntegrationType[] = ["webhook", "jira", "zentao"];

export function TicketIntegrations() {
  const { t } = useTranslation();
  const { data, isLoading } = useTicketIntegrations();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<TicketIntegration | undefined>(undefined);

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("ticketIntegrations.title")}</h1>
          <p className="text-sm text-muted-foreground">
            {t("ticketIntegrations.subtitle")}
          </p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> {t("ticketIntegrations.createButton")}
        </Button>
      </div>

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-32 w-full" />
          ) : !data || data.length === 0 ? (
            <EmptyState
              icon={<Ticket className="h-8 w-8" />}
              title={t("ticketIntegrations.emptyTitle")}
              description={t("ticketIntegrations.emptyDescription")}
            />
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">{t("ticketIntegrations.colName")}</th>
                  <th className="p-3">{t("ticketIntegrations.colType")}</th>
                  <th className="p-3">{t("ticketIntegrations.colEndpoint")}</th>
                  <th className="p-3">{t("ticketIntegrations.colTeam")}</th>
                  <th className="p-3">{t("ticketIntegrations.colStatus")}</th>
                  <th className="p-3">{t("ticketIntegrations.colCreatedAt")}</th>
                  <th className="p-3"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((ti) => (
                  <TicketIntegrationRow key={ti.id} ti={ti} onEdit={() => setEditing(ti)} />
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>

      {creating && <CreateTicketIntegrationDialog onClose={() => setCreating(false)} />}
      {editing && <EditTicketIntegrationDialog ti={editing} onClose={() => setEditing(undefined)} />}
    </div>
  );
}

/** TicketIntegrationRow 单行 + 启停/编辑/删除（凭据不显示）。 */
function TicketIntegrationRow({ ti, onEdit }: { ti: TicketIntegration; onEdit: () => void }) {
  const { t } = useTranslation();
  const del = useDeleteTicketIntegration();
  const update = useUpdateTicketIntegration();
  return (
    <tr className="border-b last:border-0">
      <td className="p-3 font-medium">{ti.name}</td>
      <td className="p-3">
        <Badge variant="secondary">{ti.type}</Badge>
      </td>
      <td className="p-3 max-w-[220px] truncate font-mono text-xs text-muted-foreground" title={ti.endpoint}>
        {ti.endpoint}
      </td>
      <td className="p-3 text-muted-foreground">
        {ti.team ? String(ti.team.name ?? `team #${ti.team.id}`) : t("ticketIntegrations.orgLevel")}
      </td>
      <td className="p-3">
        <Badge variant={ti.enabled ? "default" : "secondary"}>
          {ti.enabled ? t("ticketIntegrations.statusEnabled") : t("ticketIntegrations.statusDisabled")}
        </Badge>
      </td>
      <td className="p-3 text-muted-foreground">{formatTime(ti.created_at)}</td>
      <td className="p-3 text-right">
        <div className="flex items-center justify-end gap-1">
          <Button
            variant="ghost"
            size="sm"
            title={ti.enabled ? t("ticketIntegrations.disableAction") : t("ticketIntegrations.enableAction")}
            disabled={update.isPending}
            onClick={() => update.mutate({ id: ti.id, body: { enabled: !ti.enabled } })}
          >
            {ti.enabled ? t("ticketIntegrations.disableAction") : t("ticketIntegrations.enableAction")}
          </Button>
          <Button size="icon" variant="ghost" title={t("common.edit")} onClick={onEdit}>
            <Pencil className="h-4 w-4" />
          </Button>
          <Button
            size="icon"
            variant="ghost"
            title={t("common.delete")}
            disabled={del.isPending}
            onClick={() => del.mutate(ti.id)}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </td>
    </tr>
  );
}

/** parseConfigJSON 解析 config JSON 文本；空=不传，非法 JSON=抛错（调用方 toast）。 */
function parseConfigJSON(
  text: string,
  errMsg: string,
): Record<string, unknown> | undefined {
  const trimmed = text.trim();
  if (!trimmed) return undefined;
  const parsed = JSON.parse(trimmed);
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    throw new Error(errMsg);
  }
  return parsed as Record<string, unknown>;
}

/** CreateTicketIntegrationDialog 创建工单集成（凭据仅入不出，config 为项目/字段映射）。 */
function CreateTicketIntegrationDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const create = useCreateTicketIntegration();
  const { data: teams } = useTeams();
  const [name, setName] = useState("");
  const [type, setType] = useState<TicketIntegrationType>("webhook");
  const [endpoint, setEndpoint] = useState("");
  const [credential, setCredential] = useState("");
  const [configText, setConfigText] = useState("");
  const [teamId, setTeamId] = useState<number | undefined>(undefined);

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    let config: Record<string, unknown> | undefined;
    try {
      config = parseConfigJSON(configText, t("ticketIntegrations.configMustBeObject"));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("ticketIntegrations.configInvalidJson"));
      return;
    }
    create.mutate(
      {
        name,
        type,
        endpoint,
        credential: credential || undefined,
        config,
        team_id: teamId,
      },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("ticketIntegrations.createTitle")}
      description={t("ticketIntegrations.createDescription")}
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("ticketIntegrations.fieldName")}</label>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="jira-ops" required autoFocus />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("ticketIntegrations.fieldType")}</label>
            <Select value={type} onChange={(e) => setType(e.target.value as TicketIntegrationType)}>
              {TYPE_OPTIONS.map((opt) => (
                <option key={opt} value={opt}>{opt}</option>
              ))}
            </Select>
          </div>
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("ticketIntegrations.fieldEndpoint")}</label>
          <Input
            value={endpoint}
            onChange={(e) => setEndpoint(e.target.value)}
            placeholder="https://jira.example.com/rest/api/2/issue"
            required
          />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("ticketIntegrations.fieldCredential")}</label>
          <Input
            value={credential}
            onChange={(e) => setCredential(e.target.value)}
            type="password"
            placeholder={t("ticketIntegrations.credentialPlaceholder")}
          />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("ticketIntegrations.fieldConfig")}</label>
          <Textarea
            value={configText}
            onChange={(e) => setConfigText(e.target.value)}
            placeholder={'{"project": "OPS", "issue_type": "Bug"}'}
            rows={3}
            className="font-mono text-xs"
          />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("ticketIntegrations.fieldTeam")}</label>
          <Select
            value={teamId ? String(teamId) : ""}
            onChange={(e) => setTeamId(e.target.value ? Number(e.target.value) : undefined)}
          >
            <option value="">{t("ticketIntegrations.orgLevelOption")}</option>
            {teams?.map((team) => (
              <option key={team.id} value={team.id}>{team.name}</option>
            ))}
          </Select>
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" disabled={create.isPending || !name || !endpoint}>
            {create.isPending ? t("common.submitting") : t("common.create")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** EditTicketIntegrationDialog 编辑工单集成（type 创建后不可改；凭据留空=不改）。 */
function EditTicketIntegrationDialog({ ti, onClose }: { ti: TicketIntegration; onClose: () => void }) {
  const { t } = useTranslation();
  const update = useUpdateTicketIntegration();
  const [name, setName] = useState(ti.name);
  const [endpoint, setEndpoint] = useState(ti.endpoint);
  const [credential, setCredential] = useState("");
  const [configText, setConfigText] = useState(
    ti.config && Object.keys(ti.config).length > 0 ? JSON.stringify(ti.config, null, 2) : "",
  );
  const [enabled, setEnabled] = useState(!!ti.enabled);

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    let config: Record<string, unknown> | undefined;
    try {
      config = parseConfigJSON(configText, t("ticketIntegrations.configMustBeObject"));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("ticketIntegrations.configInvalidJson"));
      return;
    }
    // credential 留空则不传（保留原凭据）；填了则重加密替换。
    const body: Parameters<typeof update.mutate>[0]["body"] = { name, endpoint, enabled, config };
    if (credential) body.credential = credential;
    update.mutate({ id: ti.id, body }, { onSuccess: onClose });
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("ticketIntegrations.editTitle", { type: ti.type })}
      description={t("ticketIntegrations.editDescription")}
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("ticketIntegrations.fieldName")}</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("ticketIntegrations.fieldEndpoint")}</label>
          <Input value={endpoint} onChange={(e) => setEndpoint(e.target.value)} required />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("ticketIntegrations.fieldNewCredential")}</label>
          <Input
            value={credential}
            onChange={(e) => setCredential(e.target.value)}
            type="password"
            placeholder={t("ticketIntegrations.newCredentialPlaceholder")}
          />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("ticketIntegrations.fieldConfig")}</label>
          <Textarea
            value={configText}
            onChange={(e) => setConfigText(e.target.value)}
            placeholder={'{"project": "OPS", "issue_type": "Bug"}'}
            rows={3}
            className="font-mono text-xs"
          />
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            className="h-4 w-4"
          />
          <span>{t("ticketIntegrations.enabledCheckbox")}</span>
        </label>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" disabled={update.isPending || !name || !endpoint}>
            {update.isPending ? t("common.submitting") : t("common.save")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
