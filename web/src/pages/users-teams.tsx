/**
 * 用户与团队管理页（能力域 13）。
 * 单页面内用 Tabs 切换"用户"/"团队"两个视图，避免导航项过多。
 */
import { useState } from "react";
import { Building2, Plus, Trash2, Users } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs } from "@/components/ui/tabs";
import { useCreateTeam, useDeleteTeam, useTeams, useUpdateUser, useUsers } from "@/hooks/users-teams";
import { formatTime } from "@/lib/format";

export function UsersTeams() {
  const [tab, setTab] = useState("users");
  return (
    <div className="space-y-4 p-6">
      <div>
        <h1 className="text-lg font-semibold">用户与团队</h1>
        <p className="text-sm text-muted-foreground">成员启停 · 团队管理</p>
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

/** UsersTab 用户列表 + 启停切换。 */
function UsersTab() {
  const { data, isLoading } = useUsers();
  const update = useUpdateUser();

  const toggleStatus = (id: number, current: string) => {
    update.mutate({ id, body: { status: current === "active" ? "disabled" : "active" } });
  };

  return (
    <Card>
      <CardContent className="p-0">
        {isLoading ? (
          <Skeleton className="h-32 w-full" />
        ) : !data || data.length === 0 ? (
          <EmptyState icon={<Users className="h-8 w-8" />} title="暂无用户" description="登录后自动创建。" />
        ) : (
          <table className="w-full text-sm">
            <thead className="border-b text-left text-xs text-muted-foreground">
              <tr>
                <th className="p-3">用户名</th>
                <th className="p-3">邮箱</th>
                <th className="p-3">状态</th>
                <th className="p-3">创建时间</th>
                <th className="p-3"></th>
              </tr>
            </thead>
            <tbody>
              {data.map((u) => (
                <tr key={u.id} className="border-b last:border-0">
                  <td className="p-3 font-medium">{u.username}</td>
                  <td className="p-3 text-muted-foreground">{u.email}</td>
                  <td className="p-3">
                    <Badge variant={u.status === "active" ? "default" : "secondary"}>{u.status}</Badge>
                  </td>
                  <td className="p-3 text-muted-foreground">{formatTime(u.created_at)}</td>
                  <td className="p-3 text-right">
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={update.isPending}
                      onClick={() => toggleStatus(u.id, u.status ?? "active")}
                    >
                      {u.status === "active" ? "停用" : "启用"}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </CardContent>
    </Card>
  );
}

/** TeamsTab 团队列表 + 创建/删除。 */
function TeamsTab() {
  const { data, isLoading } = useTeams();
  const del = useDeleteTeam();
  const [creating, setCreating] = useState(false);

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
                    <td className="p-3 text-right">
                      <Button size="icon" variant="ghost" disabled={del.isPending} onClick={() => del.mutate(t.id)}>
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
      {creating && <CreateTeamDialog onClose={() => setCreating(false)} />}
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
        <Button type="submit" className="w-full" disabled={create.isPending || !name || !slug}>
          {create.isPending ? "创建中..." : "创建"}
        </Button>
      </form>
    </Dialog>
  );
}
