/**
 * 凭据托管页（能力域 6，Runbook/工单执行器凭据）。
 * 列出凭据元数据（name/type/team，密文永不回显）+ 创建（明文 secret 仅创建时可填）+ 删除。
 * 后端：GET/POST/PATCH/DELETE /credentials（list/get 只返元数据，密文经 Sensitive 恒不回显）。
 * 仿 services.tsx / integrations.tsx 模式。
 */
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound, Pencil, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useCreateCredential,
  useCredentials,
  useDeleteCredential,
  useUpdateCredential,
} from "@/hooks/credentials";
import { useTeams } from "@/hooks/users-teams";
import { formatTime } from "@/lib/format";
import type { Credential, CredentialType } from "@/lib/types";

const TYPE_OPTIONS: CredentialType[] = ["bearer", "token", "basic", "header"];

export function Credentials() {
  const { t } = useTranslation();
  const { data, isLoading } = useCredentials();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Credential | undefined>(undefined);

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("credentials.title")}</h1>
          <p className="text-sm text-muted-foreground">
            {t("credentials.subtitle")}
          </p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> {t("credentials.create")}
        </Button>
      </div>

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-32 w-full" />
          ) : !data || data.length === 0 ? (
            <EmptyState
              icon={<KeyRound className="h-8 w-8" />}
              title={t("credentials.emptyTitle")}
              description={t("credentials.emptyDescription")}
            />
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">{t("credentials.colName")}</th>
                  <th className="p-3">{t("credentials.colType")}</th>
                  <th className="p-3">{t("credentials.colTeam")}</th>
                  <th className="p-3">{t("credentials.colCreatedAt")}</th>
                  <th className="p-3"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((cred) => (
                  <CredentialRow key={cred.id} cred={cred} onEdit={() => setEditing(cred)} />
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>

      {creating && <CreateCredentialDialog onClose={() => setCreating(false)} />}
      {editing && <EditCredentialDialog cred={editing} onClose={() => setEditing(undefined)} />}
    </div>
  );
}

/** CredentialRow 单行 + 编辑/删除（密文不显示）。 */
function CredentialRow({ cred, onEdit }: { cred: Credential; onEdit: () => void }) {
  const { t } = useTranslation();
  const del = useDeleteCredential();
  return (
    <tr className="border-b last:border-0">
      <td className="p-3 font-medium">{cred.name}</td>
      <td className="p-3">
        <Badge variant="secondary">{cred.type}</Badge>
      </td>
      <td className="p-3 text-muted-foreground">
        {cred.team ? String(cred.team.name ?? `team #${cred.team.id}`) : t("credentials.orgLevel")}
      </td>
      <td className="p-3 text-muted-foreground">{formatTime(cred.created_at)}</td>
      <td className="p-3 text-right">
        <div className="flex items-center justify-end gap-1">
          <Button size="icon" variant="ghost" title={t("common.edit")} onClick={onEdit}>
            <Pencil className="h-4 w-4" />
          </Button>
          <Button
            size="icon"
            variant="ghost"
            title={t("common.delete")}
            disabled={del.isPending}
            onClick={() => del.mutate(cred.id)}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </td>
    </tr>
  );
}

/** CreateCredentialDialog 创建凭据（明文 secret 加密后落库，之后不回显）。 */
function CreateCredentialDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const create = useCreateCredential();
  const { data: teams } = useTeams();
  const [name, setName] = useState("");
  const [type, setType] = useState<CredentialType>("bearer");
  const [secret, setSecret] = useState("");
  const [teamId, setTeamId] = useState<number | undefined>(undefined);

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate(
      { name, type, secret, team_id: teamId },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("credentials.createTitle")}
      description={t("credentials.createDescription")}
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("credentials.fieldName")}</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="jenkins-prod-token" required autoFocus />
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("credentials.fieldType")}</label>
            <Select value={type} onChange={(e) => setType(e.target.value as CredentialType)}>
              {TYPE_OPTIONS.map((opt) => (
                <option key={opt} value={opt}>{opt}</option>
              ))}
            </Select>
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("credentials.fieldTeam")}</label>
            <Select
              value={teamId ? String(teamId) : ""}
              onChange={(e) => setTeamId(e.target.value ? Number(e.target.value) : undefined)}
            >
              <option value="">{t("credentials.teamNoneOption")}</option>
              {teams?.map((team) => (
                <option key={team.id} value={team.id}>{team.name}</option>
              ))}
            </Select>
          </div>
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("credentials.fieldSecret")}</label>
          <Input
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            type="password"
            placeholder={t("credentials.secretPlaceholder")}
            required
          />
          <p className="text-xs text-muted-foreground">{t("credentials.secretHint")}</p>
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" disabled={create.isPending || !name || !secret}>
            {create.isPending ? t("common.submitting") : t("common.create")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** EditCredentialDialog 编辑凭据（改名/类型；secret 留空=不改，填了则重加密替换）。 */
function EditCredentialDialog({ cred, onClose }: { cred: Credential; onClose: () => void }) {
  const { t } = useTranslation();
  const update = useUpdateCredential();
  const [name, setName] = useState(cred.name);
  const [type, setType] = useState<CredentialType>(cred.type);
  const [secret, setSecret] = useState("");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    // secret 留空则不传（保留原密文）；填了则重加密替换。
    const body: Parameters<typeof update.mutate>[0]["body"] = { name, type };
    if (secret) body.secret = secret;
    update.mutate({ id: cred.id, body }, { onSuccess: onClose });
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("credentials.editTitle", { name: cred.name })}
      description={t("credentials.editDescription")}
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("credentials.fieldName")}</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("credentials.fieldType")}</label>
          <Select value={type} onChange={(e) => setType(e.target.value as CredentialType)}>
            {TYPE_OPTIONS.map((opt) => (
              <option key={opt} value={opt}>{opt}</option>
            ))}
          </Select>
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("credentials.fieldNewSecret")}</label>
          <Input
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            type="password"
            placeholder={t("credentials.newSecretPlaceholder")}
          />
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" disabled={update.isPending || !name}>
            {update.isPending ? t("common.submitting") : t("common.save")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
