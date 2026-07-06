/**
 * 凭据托管页（能力域 6，Runbook/工单执行器凭据）。
 * 列出凭据元数据（name/type/team，密文永不回显）+ 创建（明文 secret 仅创建时可填）+ 删除。
 * 后端：GET/POST/PATCH/DELETE /credentials（list/get 只返元数据，密文经 Sensitive 恒不回显）。
 * 仿 services.tsx / integrations.tsx 模式。
 */
import { useState } from "react";
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
  const { data, isLoading } = useCredentials();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Credential | undefined>(undefined);

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">凭据托管</h1>
          <p className="text-sm text-muted-foreground">
            Runbook / 工单执行器凭据（加密存储，密文仅创建时可填、之后不可见）。
          </p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> 创建凭据
        </Button>
      </div>

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-32 w-full" />
          ) : !data || data.length === 0 ? (
            <EmptyState
              icon={<KeyRound className="h-8 w-8" />}
              title="暂无凭据"
              description="创建凭据后，Runbook / 工单执行器引用其名即可使用（密文加密托管）。"
            />
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">名称</th>
                  <th className="p-3">类型</th>
                  <th className="p-3">归属团队</th>
                  <th className="p-3">创建时间</th>
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
  const del = useDeleteCredential();
  return (
    <tr className="border-b last:border-0">
      <td className="p-3 font-medium">{cred.name}</td>
      <td className="p-3">
        <Badge variant="secondary">{cred.type}</Badge>
      </td>
      <td className="p-3 text-muted-foreground">
        {cred.team ? String(cred.team.name ?? `team #${cred.team.id}`) : "组织级"}
      </td>
      <td className="p-3 text-muted-foreground">{formatTime(cred.created_at)}</td>
      <td className="p-3 text-right">
        <div className="flex items-center justify-end gap-1">
          <Button size="icon" variant="ghost" title="编辑" onClick={onEdit}>
            <Pencil className="h-4 w-4" />
          </Button>
          <Button
            size="icon"
            variant="ghost"
            title="删除"
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
      title="创建凭据"
      description="⚠️ 密文仅创建时可填、加密存储后永不回显。请妥善来源保管。"
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="jenkins-prod-token" required autoFocus />
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">类型</label>
            <Select value={type} onChange={(e) => setType(e.target.value as CredentialType)}>
              {TYPE_OPTIONS.map((t) => (
                <option key={t} value={t}>{t}</option>
              ))}
            </Select>
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">归属团队（留空=组织级）</label>
            <Select
              value={teamId ? String(teamId) : ""}
              onChange={(e) => setTeamId(e.target.value ? Number(e.target.value) : undefined)}
            >
              <option value="">组织级（无归属）</option>
              {teams?.map((t) => (
                <option key={t.id} value={t.id}>{t.name}</option>
              ))}
            </Select>
          </div>
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">密文（secret）</label>
          <Input
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            type="password"
            placeholder="加密存储，之后不可见"
            required
          />
          <p className="text-xs text-muted-foreground">密文加密后落库，列表/编辑均不回显。</p>
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>取消</Button>
          <Button type="submit" disabled={create.isPending || !name || !secret}>
            {create.isPending ? "创建中..." : "创建"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** EditCredentialDialog 编辑凭据（改名/类型；secret 留空=不改，填了则重加密替换）。 */
function EditCredentialDialog({ cred, onClose }: { cred: Credential; onClose: () => void }) {
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
      title={`编辑凭据 · ${cred.name}`}
      description="密文留空表示保留原值；填写则重加密替换（旧密文永不回显）。"
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">类型</label>
          <Select value={type} onChange={(e) => setType(e.target.value as CredentialType)}>
            {TYPE_OPTIONS.map((t) => (
              <option key={t} value={t}>{t}</option>
            ))}
          </Select>
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">新密文（留空=不修改）</label>
          <Input
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            type="password"
            placeholder="留空则保留原密文"
          />
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>取消</Button>
          <Button type="submit" disabled={update.isPending || !name}>
            {update.isPending ? "保存中..." : "保存"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
