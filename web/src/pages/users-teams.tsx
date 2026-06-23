/**
 * 用户与团队管理页（能力域 13）。
 * 单页面内用 Tabs 切换"用户"/"团队"两个视图，避免导航项过多。
 *
 * 用户：列表 + 启停 + 改名/时区 + 角色分配（接 POST/DELETE /role-bindings）。
 * 团队：列表 + 创建/编辑/删除。
 */
import { useMemo, useState } from "react";
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
function roleName(r: RoleBinding["role"]): string {
  if (!r) return "角色#?";
  return String(r.name ?? "") || `角色#${r.id ?? "?"}`;
}

export function UsersTeams() {
  const [tab, setTab] = useState("users");
  return (
    <div className="space-y-4 p-6">
      <div>
        <h1 className="text-lg font-semibold">用户与团队</h1>
        <p className="text-sm text-muted-foreground">成员启停 · 角色分配 · 团队管理</p>
      </div>
      <Tabs
        value={tab}
        onValueChange={setTab}
        items={[{ value: "users", label: "用户" }, { value: "teams", label: "团队" }]}
      />
      {tab === "users" && <UsersTab />}
      {tab === "teams" && <TeamsTab />}
    </div>
  );
}

/** UsersTab 用户列表 + 启停/编辑/角色分配。 */
function UsersTab() {
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
            title="暂无用户"
            description="用户在首次登录后自动创建（设计基线：登录即建号）。"
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">用户名</th>
                  <th className="p-3">邮箱</th>
                  <th className="p-3">角色</th>
                  <th className="p-3">状态</th>
                  <th className="p-3">创建时间</th>
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
                                {roleName(b.role)}
                                {b.team_id && <span className="ml-1 opacity-60">·team</span>}
                                {b.expires_at && <span className="ml-1 opacity-60">·临时</span>}
                              </Badge>
                            ))
                          )}
                        </div>
                      </td>
                      <td className="p-3">
                        <Badge variant={u.status === "active" ? "default" : "secondary"}>
                          {u.status}
                        </Badge>
                      </td>
                      <td className="p-3 text-muted-foreground">{formatTime(u.created_at)}</td>
                      <td className="p-3">
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            size="sm"
                            variant="ghost"
                            title="分配角色"
                            onClick={() => setRoleUser(u)}
                          >
                            <UserCog className="h-4 w-4" />
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            title="编辑"
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
                            {u.status === "active" ? "停用" : "启用"}
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
  const update = useUpdateUser();
  const [name, setName] = useState(user.name ?? "");
  const [timezone, setTimezone] = useState(user.timezone ?? "Asia/Shanghai");

  return (
    <Dialog open onClose={onClose} title={`编辑用户 · ${user.username}`} description="修改显示名与时区。密码变更走独立流程。">
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          update.mutate({ id: user.id, body: { name, timezone } }, { onSuccess: onClose });
        }}
      >
        <div className="space-y-1.5">
          <label className="text-sm font-medium">显示名</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="张三" autoFocus />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">时区</label>
          <Input value={timezone} onChange={(e) => setTimezone(e.target.value)} placeholder="Asia/Shanghai" />
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>取消</Button>
          <Button type="submit" disabled={update.isPending}>保存</Button>
        </div>
      </form>
    </Dialog>
  );
}

/** RoleAssignDialog 给用户分配/撤销角色（POST/DELETE /role-bindings）。 */
function RoleAssignDialog({ user, onClose }: { user: User; onClose: () => void }) {
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
    <Dialog open onClose={onClose} title={`角色分配 · ${user.username}`} description="授予角色（含临时授权）。授权会立即生效。">
      <div className="space-y-4">
        {/* 当前授权 */}
        <div className="space-y-1.5">
          <label className="text-sm font-medium">当前角色</label>
          {myBindings.length === 0 ? (
            <p className="text-xs text-muted-foreground">暂无角色。</p>
          ) : (
            <div className="space-y-1.5">
              {myBindings.map((b) => (
                <div key={b.id} className="flex items-center justify-between rounded-md border p-2 text-sm">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{roleName(b.role)}</span>
                    <Badge variant="outline" className="text-xs">{b.scope_level}</Badge>
                    {b.expires_at && (
                      <Badge variant="secondary" className="text-xs">
                        至 {formatTime(b.expires_at)}
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
          <label className="text-sm font-medium">新增授权</label>
          <Select
            value={roleId ? String(roleId) : ""}
            onChange={(e) => setRoleId(e.target.value ? Number(e.target.value) : undefined)}
          >
            <option value="">选择角色…</option>
            {roles?.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name}（{r.scope_level}）
              </option>
            ))}
          </Select>
          <div className="grid grid-cols-2 gap-2">
            <div className="space-y-1.5">
              <label className="text-xs font-medium text-muted-foreground">作用域</label>
              <Select value={scope} onChange={(e) => setScope(e.target.value as "org" | "team")}>
                <option value="org">组织（org）</option>
                <option value="team">团队（team）</option>
              </Select>
            </div>
            <div className="space-y-1.5">
              <label className="text-xs font-medium text-muted-foreground">有效期（小时，留空=永久）</label>
              <Input
                type="number"
                min={1}
                value={expiresIn}
                onChange={(e) => setExpiresIn(e.target.value)}
                placeholder="永久"
              />
            </div>
          </div>
          {create.isError && (
            <p className="text-xs text-destructive">{extractError(create.error)}</p>
          )}
          <Button type="submit" className="w-full" disabled={create.isPending || !roleId}>
            授权
          </Button>
        </form>
      </div>
    </Dialog>
  );
}

/** TeamsTab 团队列表 + 创建/编辑/删除。 */
function TeamsTab() {
  const { data, isLoading } = useTeams();
  const del = useDeleteTeam();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Team | undefined>(undefined);

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Button size="sm" onClick={() => setCreating(true)}>
          <Plus className="mr-1 h-4 w-4" /> 创建团队
        </Button>
      </div>
      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-24 w-full" />
          ) : !data || data.length === 0 ? (
            <EmptyState icon={<Building2 className="h-8 w-8" />} title="暂无团队" description="创建团队以组织成员与服务。" />
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">名称</th>
                  <th className="p-3">Slug</th>
                  <th className="p-3">描述</th>
                  <th className="p-3"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((t) => (
                  <tr key={t.id} className="border-b last:border-0">
                    <td className="p-3 font-medium">{t.name}</td>
                    <td className="p-3 font-mono text-xs text-muted-foreground">{t.slug}</td>
                    <td className="p-3 text-muted-foreground">{t.description || "—"}</td>
                    <td className="p-3">
                      <div className="flex items-center justify-end gap-1">
                        <Button size="icon" variant="ghost" title="编辑" onClick={() => setEditing(t)}>
                          <Pencil className="h-4 w-4" />
                        </Button>
                        <Button
                          size="icon"
                          variant="ghost"
                          disabled={del.isPending}
                          onClick={() => {
                            if (confirm(`确认删除团队「${t.name}」？`)) del.mutate(t.id);
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
  const create = useCreateTeam();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    create.mutate({ name, slug, description }, { onSuccess: onClose });
  };

  return (
    <Dialog open onClose={onClose} title="创建团队" description="团队用于组织成员与服务，权限按团队作用域。">
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="SRE 平台组" required autoFocus />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">Slug（唯一标识）</label>
          <Input value={slug} onChange={(e) => setSlug(e.target.value)} placeholder="sre-platform" required />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">描述</label>
          <Input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="（可选）" />
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>取消</Button>
          <Button type="submit" disabled={create.isPending || !name || !slug}>
            {create.isPending ? "创建中..." : "创建"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** EditTeamDialog 编辑团队（名称/描述，slug 不可改）。 */
function EditTeamDialog({ team, onClose }: { team: Team; onClose: () => void }) {
  const update = useUpdateTeam();
  const [name, setName] = useState(team.name);
  const [description, setDescription] = useState(team.description ?? "");

  return (
    <Dialog open onClose={onClose} title={`编辑团队 · ${team.slug}`} description="Slug 为唯一标识，创建后不可修改。">
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          update.mutate({ id: team.id, body: { name, description } }, { onSuccess: onClose });
        }}
      >
        <div className="space-y-1.5">
          <label className="text-sm font-medium">名称</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">描述</label>
          <Input value={description} onChange={(e) => setDescription(e.target.value)} />
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" onClick={onClose}>取消</Button>
          <Button type="submit" disabled={update.isPending}>保存</Button>
        </div>
      </form>
    </Dialog>
  );
}
