/** RBAC —— RBACTab：角色 + 角色绑定 CRUD。 */
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useCreateRole,
  useCreateRoleBinding,
  useDeleteRole,
  useDeleteRoleBinding,
  useRoleBindings,
  useRoles,
} from "@/hooks/settings";
import { formatTime } from "@/lib/format";
import { useUsers } from "@/hooks/users-teams";
import type { RoleBinding } from "@/lib/types";

type TFunc = ReturnType<typeof useTranslation>["t"];

/** userName 从 RoleBinding.user edge 提取可读名（edge 带 [k:string]:unknown 索引，需收敛为 string）。 */
function userName(u: RoleBinding["user"], t: TFunc): string {
  if (!u) return "?";
  const name = String(u.name ?? u.username ?? "");
  return name || t("settings.rbac.userFallback", { id: u.id ?? "?" });
}

/** roleName 从 RoleBinding.role edge 提取可读名。 */
function roleName(r: RoleBinding["role"], t: TFunc): string {
  if (!r) return t("settings.rbac.roleFallback", { id: "?" });
  return String(r.name ?? "") || t("settings.rbac.roleFallback", { id: r.id ?? "?" });
}

export function RBACTab() {
  const { t } = useTranslation();
  const roles = useRoles();
  const bindings = useRoleBindings();
  const delRole = useDeleteRole();
  const delBinding = useDeleteRoleBinding();
  const [creatingRole, setCreatingRole] = useState(false);
  const [creatingBinding, setCreatingBinding] = useState(false);

  return (
    <div className="grid gap-4 md:grid-cols-2">
      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="text-base">{t("settings.rbac.rolesTitle")}</CardTitle>
          <Button size="sm" onClick={() => setCreatingRole(true)}>
            <Plus className="mr-1 h-4 w-4" /> {t("common.create")}
          </Button>
        </CardHeader>
        <CardContent>
          {roles.isLoading ? (
            <Skeleton className="h-20 w-full" />
          ) : !roles.data || roles.data.length === 0 ? (
            <EmptyState title={t("settings.rbac.emptyRolesTitle")} description={t("settings.rbac.emptyRolesDesc")} />
          ) : (
            <div className="space-y-2">
              {roles.data.map((r) => (
                <div key={r.id} className="flex items-center justify-between rounded-md border p-2">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium">{r.name}</span>
                      {r.builtin && <Badge variant="secondary" className="text-xs">{t("settings.rbac.builtin")}</Badge>}
                      <Badge variant="outline" className="text-xs">{r.scope_level}</Badge>
                    </div>
                    <div className="mt-1 text-xs text-muted-foreground">
                      {t("settings.rbac.permissionCount", { count: r.permissions.length })}
                      {r.description ? ` · ${r.description}` : ""}
                    </div>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    disabled={r.builtin || delRole.isPending}
                    onClick={() => delRole.mutate(r.id)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="text-base">{t("settings.rbac.bindingsTitle")}</CardTitle>
          <Button size="sm" onClick={() => setCreatingBinding(true)}>
            <Plus className="mr-1 h-4 w-4" /> {t("settings.rbac.grant")}
          </Button>
        </CardHeader>
        <CardContent>
          {bindings.isLoading ? (
            <Skeleton className="h-20 w-full" />
          ) : !bindings.data || bindings.data.length === 0 ? (
            <EmptyState title={t("settings.rbac.emptyBindingsTitle")} description={t("settings.rbac.emptyBindingsDesc")} />
          ) : (
            <div className="space-y-2">
              {bindings.data.map((b) => (
                <div key={b.id} className="flex items-center justify-between rounded-md border p-2 text-sm">
                  <div className="min-w-0">
                    <span className="font-medium">{userName(b.user, t)}</span>
                    <span className="mx-1 text-muted-foreground">→</span>
                    <span className="font-medium">{roleName(b.role, t)}</span>
                    <Badge variant="outline" className="ml-2 text-xs">{b.scope_level}</Badge>
                    {b.team_id && <span className="ml-2 text-xs text-muted-foreground">team #{b.team_id}</span>}
                    {b.expires_at && (
                      <Badge variant="secondary" className="ml-2 text-xs">{t("settings.rbac.temporary", { time: formatTime(b.expires_at) })}</Badge>
                    )}
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    disabled={delBinding.isPending}
                    onClick={() => delBinding.mutate(b.id)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      {creatingRole && <CreateRoleDialog onClose={() => setCreatingRole(false)} />}
      {creatingBinding && <CreateRoleBindingDialog onClose={() => setCreatingBinding(false)} />}
    </div>
  );
}

// 系统全部权限点（与 internal/auth/permission.go AllPermissions 对齐），按 resource 分组。
// 注：权限点为系统枚举，前端只做展示分组，新增权限点须同步后端 permission.go。
const ALL_PERMISSIONS = [
  "incident.view", "incident.create", "incident.ack", "incident.escalate", "incident.resolve",
  "incident.reopen", "incident.reassign", "incident.snooze", "incident.add_responder",
  "incident.runbook.execute", "incident.delete",
  "event.view", "event.view_unrouted",
  "service.view", "service.create", "service.update", "service.delete", "service.route_override",
  "schedule.view", "schedule.create", "schedule.update", "schedule.delete", "schedule.override",
  "escalation.view", "escalation.create", "escalation.update", "escalation.delete",
  "runbook.view", "runbook.create", "runbook.update", "runbook.delete", "runbook.execute",
  "integration.view", "integration.create", "integration.update", "integration.delete",
  "postmortem.view", "postmortem.create", "postmortem.update", "postmortem.publish", "postmortem.actionitem.manage",
  "team.view", "team.create", "team.update", "team.delete", "team.member.manage",
  "user.view", "user.create", "user.update", "user.disable", "user.im.bind",
  "role.view", "role.create", "role.update", "role.delete", "role.assign",
  "notification.rule.view", "notification.rule.create", "notification.rule.update", "notification.rule.delete",
  "notification.template.view", "notification.template.create", "notification.template.update", "notification.template.delete",
  "suppression.view", "suppression.create", "suppression.update", "suppression.delete",
  "admin.settings", "admin.audit.view", "admin.apikey.manage", "admin.global_integration",
];

/** CreateRoleDialog 创建自定义角色（权限点按 resource 分组多选）。 */
function CreateRoleDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const create = useCreateRole();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [scope, setScope] = useState<"org" | "team">("team");
  const [perms, setPerms] = useState<Set<string>>(new Set());

  // 按 resource 前缀分组：incident.* / service.* ...
  const groups = useMemo(() => {
    const m = new Map<string, string[]>();
    for (const p of ALL_PERMISSIONS) {
      const res = p.split(".")[0];
      const arr = m.get(res) ?? [];
      arr.push(p);
      m.set(res, arr);
    }
    return Array.from(m.entries()).sort((a, b) => a[0].localeCompare(b[0]));
  }, []);

  const togglePerm = (p: string) =>
    setPerms((prev) => {
      const next = new Set(prev);
      if (next.has(p)) next.delete(p);
      else next.add(p);
      return next;
    });

  const toggleGroup = (members: string[]) =>
    setPerms((prev) => {
      const allOn = members.every((m) => prev.has(m));
      const next = new Set(prev);
      if (allOn) members.forEach((m) => next.delete(m));
      else members.forEach((m) => next.add(m));
      return next;
    });

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate(
      { name, description, scope_level: scope, permissions: Array.from(perms) },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open onClose={onClose} title={t("settings.rbac.createRoleTitle")} description={t("settings.rbac.createRoleDesc")}>
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("settings.rbac.name")}</label>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="oncall-responder" required autoFocus />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("settings.rbac.scope")}</label>
            <Select value={scope} onChange={(e) => setScope(e.target.value as "org" | "team")}>
              <option value="team">{t("settings.rbac.scopeTeam")}</option>
              <option value="org">{t("settings.rbac.scopeOrg")}</option>
            </Select>
          </div>
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("settings.rbac.description")}</label>
          <Input value={description} onChange={(e) => setDescription(e.target.value)} placeholder={t("settings.rbac.optionalPlaceholder")} />
        </div>
        <div className="space-y-1.5">
          <div className="flex items-center justify-between">
            <label className="text-sm font-medium">
              {t("settings.rbac.permissions")} <span className="text-xs text-muted-foreground">{t("settings.rbac.selectedCount", { count: perms.size })}</span>
            </label>
          </div>
          <div className="max-h-64 space-y-2 overflow-auto rounded-md border p-2">
            {groups.map(([res, members]) => {
              const allOn = members.every((m) => perms.has(m));
              return (
                <div key={res}>
                  <button
                    type="button"
                    onClick={() => toggleGroup(members)}
                    className="flex w-full items-center gap-1 text-xs font-semibold uppercase text-muted-foreground hover:text-foreground"
                  >
                    <span>{res}</span>
                    <span className="font-normal normal-case opacity-60">({members.length})</span>
                    <span className="ml-auto">{allOn ? t("settings.rbac.deselectAll") : t("settings.rbac.selectAll")}</span>
                  </button>
                  <div className="mt-1 flex flex-wrap gap-1">
                    {members.map((p) => {
                      const on = perms.has(p);
                      return (
                        <button
                          key={p}
                          type="button"
                          onClick={() => togglePerm(p)}
                          className={`rounded-md border px-2 py-0.5 text-xs transition-colors ${
                            on ? "border-primary bg-primary text-primary-foreground" : "hover:bg-accent"
                          }`}
                        >
                          {p.replace(`${res}.`, "")}
                        </button>
                      );
                    })}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" disabled={create.isPending || !name}>
            {create.isPending ? t("common.submitting") : t("common.create")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** CreateRoleBindingDialog 授权：选用户 → 选角色 → 作用域/有效期。 */
function CreateRoleBindingDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const create = useCreateRoleBinding();
  const { data: users } = useUsers();
  const { data: roles } = useRoles();
  const [userId, setUserId] = useState<number | undefined>(undefined);
  const [roleId, setRoleId] = useState<number | undefined>(undefined);
  const [scope, setScope] = useState<"org" | "team">("org");
  const [expiresIn, setExpiresIn] = useState("");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!userId || !roleId) return;
    create.mutate(
      { user_id: userId, role_id: roleId, scope_level: scope, expires_in_hours: expiresIn ? Number(expiresIn) : undefined },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open onClose={onClose} title={t("settings.rbac.grantTitle")} description={t("settings.rbac.grantDesc")}>
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("settings.rbac.user")}</label>
          <Select value={userId ? String(userId) : ""} onChange={(e) => setUserId(e.target.value ? Number(e.target.value) : undefined)}>
            <option value="">{t("settings.rbac.selectUser")}</option>
            {users?.map((u) => (
              <option key={u.id} value={u.id}>
                {u.username || u.name || t("settings.rbac.userFallback", { id: u.id })}（{u.email || "—"}）
              </option>
            ))}
          </Select>
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("settings.rbac.role")}</label>
          <Select value={roleId ? String(roleId) : ""} onChange={(e) => setRoleId(e.target.value ? Number(e.target.value) : undefined)}>
            <option value="">{t("settings.rbac.selectRole")}</option>
            {roles?.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name}（{r.scope_level}）
              </option>
            ))}
          </Select>
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("settings.rbac.scope")}</label>
            <Select value={scope} onChange={(e) => setScope(e.target.value as "org" | "team")}>
              <option value="org">{t("settings.rbac.scopeOrg")}</option>
              <option value="team">{t("settings.rbac.scopeTeam")}</option>
            </Select>
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("settings.rbac.expiresLabel")}</label>
            <Input type="number" min={1} value={expiresIn} onChange={(e) => setExpiresIn(e.target.value)} placeholder={t("settings.rbac.permanent")} />
          </div>
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" disabled={create.isPending || !userId || !roleId}>
            {create.isPending ? t("settings.rbac.granting") : t("settings.rbac.grant")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
