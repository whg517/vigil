/**
 * 用户与团队管理页（能力域 13）。
 * 单页面内用 Tabs 切换"用户"/"团队"两个视图，避免导航项过多。
 *
 * 用户：列表 + 启停 + 改名/时区 + 角色分配（接 POST/DELETE /role-bindings）。
 * 团队：列表 + 创建/编辑/删除。
 */
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Building2, Pencil, Plus, Trash2, UserCog, Users } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs } from "@/components/ui/tabs";
import {
  useCreateTeam,
  useDeleteTeam,
  useTeams,
  useUpdateTeam,
  useUpdateUser,
  useUsers,
} from "@/hooks/users-teams";
import {
  useCreateRoleBinding,
  useDeleteRoleBinding,
  useRoleBindings,
  useRoles,
} from "@/hooks/settings";
import { extractError } from "@/lib/http";
import { formatTime } from "@/lib/format";
import type { RoleBinding, Team, User } from "@/lib/types";

/** roleName 从 RoleBinding.role edge 提取可读名（edge 带 [k:string]:unknown 索引，需收敛为 string）。 */
function roleName(r: RoleBinding["role"], unknownLabel: string): string {
  if (!r) return unknownLabel;
  return String(r.name ?? "") || `${unknownLabel}#${r.id ?? "?"}`;
}

export function UsersTeams() {
  const { t } = useTranslation();
  const [tab, setTab] = useState("users");
  return (
    <div className="space-y-4 p-6">
      <div>
        <h1 className="text-lg font-semibold">{t("usersTeams.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("usersTeams.subtitle")}</p>
      </div>
      <Tabs
        value={tab}
        onValueChange={setTab}
        items={[
          { value: "users", label: t("usersTeams.tabUsers") },
          { value: "teams", label: t("usersTeams.tabTeams") },
        ]}
      />
      {tab === "users" && <UsersTab />}
      {tab === "teams" && <TeamsTab />}
    </div>
  );
}

/** UsersTab 用户列表 + 启停/编辑/角色分配。 */
function UsersTab() {
  const { t } = useTranslation();
  const { data, isLoading } = useUsers();
  const { data: bindings } = useRoleBindings();
  const update = useUpdateUser();
  const [editing, setEditing] = useState<User | undefined>(undefined);
  const [roleUser, setRoleUser] = useState<User | undefined>(undefined);

  // 按 user_id 聚合角色绑定
  const bindingsByUser = useMemo(() => {
    const m = new Map<number, RoleBinding[]>();
    for (const b of bindings ?? []) {
      const uid = b.user?.id;
      if (!uid) continue;
      const arr = m.get(uid) ?? [];
      arr.push(b);
      m.set(uid, arr);
    }
    return m;
  }, [bindings]);

  const toggleStatus = (u: User) => {
    update.mutate({ id: u.id, body: { status: u.status === "active" ? "disabled" : "active" } });
  };

  return (
    <Card>
      <CardContent className="p-0">
        {isLoading ? (
          <Skeleton className="h-32 w-full" />
        ) : !data || data.length === 0 ? (
          <EmptyState
            icon={<Users className="h-8 w-8" />}
            title={t("usersTeams.usersEmpty")}
            description={t("usersTeams.usersEmptyHint")}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">{t("usersTeams.colUsername")}</th>
                  <th className="p-3">{t("usersTeams.colEmail")}</th>
                  <th className="p-3">{t("usersTeams.colRoles")}</th>
                  <th className="p-3">{t("usersTeams.colStatus")}</th>
                  <th className="p-3">{t("usersTeams.colCreatedAt")}</th>
                  <th className="p-3"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((u) => {
                  const roles = bindingsByUser.get(u.id) ?? [];
                  return (
                    <tr key={u.id} className="border-b last:border-0">
                      <td className="p-3 font-medium">{u.username || u.name || "—"}</td>
                      <td className="p-3 text-muted-foreground">{u.email || "—"}</td>
                      <td className="p-3">
                        <div className="flex flex-wrap gap-1">
                          {roles.length === 0 ? (
                            <span className="text-xs text-muted-foreground">—</span>
                          ) : (
                            roles.map((b) => (
                              <Badge key={b.id} variant="secondary" className="text-xs">
                                {roleName(b.role, t("usersTeams.roleUnknown"))}
                                {b.team_id && (
                                  <span className="ml-1 opacity-60">·{t("usersTeams.scopeTeam")}</span>
                                )}
                                {b.expires_at && (
                                  <span className="ml-1 opacity-60">·{t("usersTeams.temporary")}</span>
                                )}
                              </Badge>
                            ))
                          )}
                        </div>
                      </td>
                      <td className="p-3">
                        <Badge variant={u.status === "active" ? "default" : "secondary"}>
                          {u.status === "active"
                            ? t("usersTeams.statusActive")
                            : t("usersTeams.statusDisabled")}
                        </Badge>
                      </td>
                      <td className="p-3 text-muted-foreground">{formatTime(u.created_at)}</td>
                      <td className="p-3">
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            size="sm"
                            variant="ghost"
                            title={t("usersTeams.assignRole")}
                            onClick={() => setRoleUser(u)}
                          >
                            <UserCog className="h-4 w-4" />
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            title={t("common.edit")}
                            onClick={() => setEditing(u)}
                          >
                            <Pencil className="h-4 w-4" />
                          </Button>
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={update.isPending}
                            onClick={() => toggleStatus(u)}
                          >
                            {u.status === "active"
                              ? t("usersTeams.disable")
                              : t("usersTeams.enable")}
                          </Button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
      {editing && <EditUserDialog user={editing} onClose={() => setEditing(undefined)} />}
      {roleUser && <RoleAssignDialog user={roleUser} onClose={() => setRoleUser(undefined)} />}
    </Card>
  );
}

/** EditUserDialog 编辑用户显示名/时区（不改密码，密码走独立流程）。 */
function EditUserDialog({ user, onClose }: { user: User; onClose: () => void }) {
  const { t } = useTranslation();
  const update = useUpdateUser();
  const [name, setName] = useState(user.name ?? "");
  const [timezone, setTimezone] = useState(user.timezone ?? "Asia/Shanghai");

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("usersTeams.editUserTitle", { name: user.username })}
      description={t("usersTeams.editUserDesc")}
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          update.mutate({ id: user.id, body: { name, timezone } }, { onSuccess: onClose });
        }}
      >
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("usersTeams.displayName")}</label>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("usersTeams.displayNamePlaceholder")}
            autoFocus
          />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("usersTeams.timezone")}</label>
          <Input value={timezone} onChange={(e) => setTimezone(e.target.value)} placeholder="Asia/Shanghai" />
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" disabled={update.isPending}>{t("common.save")}</Button>
        </div>
      </form>
    </Dialog>
  );
}

/** RoleAssignDialog 给用户分配/撤销角色（POST/DELETE /role-bindings）。 */
function RoleAssignDialog({ user, onClose }: { user: User; onClose: () => void }) {
  const { t } = useTranslation();
  const { data: roles } = useRoles();
  const { data: bindings } = useRoleBindings();
  const create = useCreateRoleBinding();
  const del = useDeleteRoleBinding();

  const myBindings = (bindings ?? []).filter((b) => b.user?.id === user.id);

  const [roleId, setRoleId] = useState<number | undefined>(undefined);
  const [scope, setScope] = useState<"org" | "team">("org");
  const [expiresIn, setExpiresIn] = useState("");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!roleId) return;
    create.mutate(
      {
        user_id: user.id,
        role_id: roleId,
        scope_level: scope,
        expires_in_hours: expiresIn ? Number(expiresIn) : undefined,
      },
      {
        onSuccess: () => setRoleId(undefined),
        onError: () => {},
      },
    );
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("usersTeams.roleAssignTitle", { name: user.username })}
      description={t("usersTeams.roleAssignDesc")}
    >
      <div className="space-y-4">
        {/* 当前授权 */}
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("usersTeams.currentRoles")}</label>
          {myBindings.length === 0 ? (
            <p className="text-xs text-muted-foreground">{t("usersTeams.noRoles")}</p>
          ) : (
            <div className="space-y-1.5">
              {myBindings.map((b) => (
                <div key={b.id} className="flex items-center justify-between rounded-md border p-2 text-sm">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{roleName(b.role, t("usersTeams.roleUnknown"))}</span>
                    <Badge variant="outline" className="text-xs">{b.scope_level}</Badge>
                    {b.expires_at && (
                      <Badge variant="secondary" className="text-xs">
                        {t("usersTeams.until", { time: formatTime(b.expires_at) })}
                      </Badge>
                    )}
                  </div>
                  <Button
                    size="icon"
                    variant="ghost"
                    disabled={del.isPending}
                    onClick={() => del.mutate(b.id)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
          )}
        </div>

        {/* 新增授权 */}
        <form className="space-y-3 border-t pt-3" onSubmit={onSubmit}>
          <label className="text-sm font-medium">{t("usersTeams.addGrant")}</label>
          <Select
            value={roleId ? String(roleId) : ""}
            onChange={(e) => setRoleId(e.target.value ? Number(e.target.value) : undefined)}
          >
            <option value="">{t("usersTeams.selectRole")}</option>
            {roles?.map((r) => (
              <option key={r.id} value={r.id}>
                {t("usersTeams.roleOption", { name: r.name, scope: r.scope_level })}
              </option>
            ))}
          </Select>
          <div className="grid grid-cols-2 gap-2">
            <div className="space-y-1.5">
              <label className="text-xs font-medium text-muted-foreground">{t("usersTeams.scope")}</label>
              <Select value={scope} onChange={(e) => setScope(e.target.value as "org" | "team")}>
                <option value="org">{t("usersTeams.scopeOrgOption")}</option>
                <option value="team">{t("usersTeams.scopeTeamOption")}</option>
              </Select>
            </div>
            <div className="space-y-1.5">
              <label className="text-xs font-medium text-muted-foreground">{t("usersTeams.expiresLabel")}</label>
              <Input
                type="number"
                min={1}
                value={expiresIn}
                onChange={(e) => setExpiresIn(e.target.value)}
                placeholder={t("usersTeams.permanent")}
              />
            </div>
          </div>
          {create.isError && (
            <p className="text-xs text-destructive">{extractError(create.error)}</p>
          )}
          <Button type="submit" className="w-full" disabled={create.isPending || !roleId}>
            {t("usersTeams.grant")}
          </Button>
        </form>
      </div>
    </Dialog>
  );
}

/** TeamsTab 团队列表 + 创建/编辑/删除。 */
function TeamsTab() {
  const { t } = useTranslation();
  const { data, isLoading } = useTeams();
  const del = useDeleteTeam();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Team | undefined>(undefined);

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Button size="sm" onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> {t("usersTeams.createTeam")}
        </Button>
      </div>
      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-24 w-full" />
          ) : !data || data.length === 0 ? (
            <EmptyState
              icon={<Building2 className="h-8 w-8" />}
              title={t("usersTeams.teamsEmpty")}
              description={t("usersTeams.teamsEmptyHint")}
            />
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">{t("usersTeams.colName")}</th>
                  <th className="p-3">{t("usersTeams.colSlug")}</th>
                  <th className="p-3">{t("usersTeams.colDescription")}</th>
                  <th className="p-3"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((team) => (
                  <tr key={team.id} className="border-b last:border-0">
                    <td className="p-3 font-medium">{team.name}</td>
                    <td className="p-3 font-mono text-xs text-muted-foreground">{team.slug}</td>
                    <td className="p-3 text-muted-foreground">{team.description || "—"}</td>
                    <td className="p-3">
                      <div className="flex items-center justify-end gap-1">
                        <Button size="icon" variant="ghost" title={t("common.edit")} onClick={() => setEditing(team)}>
                          <Pencil className="h-4 w-4" />
                        </Button>
                        <Button
                          size="icon"
                          variant="ghost"
                          disabled={del.isPending}
                          onClick={() => {
                            if (confirm(t("usersTeams.confirmDeleteTeam", { name: team.name }))) del.mutate(team.id);
                          }}
                        >
                          <Trash2 className="h-4 w-4" />
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>
      {creating && <CreateTeamDialog onClose={() => setCreating(false)} />}
      {editing && <EditTeamDialog team={editing} onClose={() => setEditing(undefined)} />}
    </div>
  );
}

/** CreateTeamDialog 创建团队。 */
function CreateTeamDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const create = useCreateTeam();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate({ name, slug, description }, { onSuccess: onClose });
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("usersTeams.createTeamTitle")}
      description={t("usersTeams.createTeamDesc")}
    >
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("usersTeams.teamName")}</label>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("usersTeams.teamNamePlaceholder")}
            required
            autoFocus
          />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("usersTeams.teamSlug")}</label>
          <Input value={slug} onChange={(e) => setSlug(e.target.value)} placeholder="sre-platform" required />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("usersTeams.teamDescription")}</label>
          <Input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder={t("usersTeams.optional")}
          />
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" disabled={create.isPending || !name || !slug}>
            {create.isPending ? t("usersTeams.creating") : t("common.create")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** EditTeamDialog 编辑团队（名称/描述，slug 不可改）。 */
function EditTeamDialog({ team, onClose }: { team: Team; onClose: () => void }) {
  const { t } = useTranslation();
  const update = useUpdateTeam();
  const [name, setName] = useState(team.name);
  const [description, setDescription] = useState(team.description ?? "");

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("usersTeams.editTeamTitle", { slug: team.slug })}
      description={t("usersTeams.editTeamDesc")}
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          update.mutate({ id: team.id, body: { name, description } }, { onSuccess: onClose });
        }}
      >
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("usersTeams.teamName")}</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("usersTeams.teamDescription")}</label>
          <Input value={description} onChange={(e) => setDescription(e.target.value)} />
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" disabled={update.isPending}>{t("common.save")}</Button>
        </div>
      </form>
    </Dialog>
  );
}
