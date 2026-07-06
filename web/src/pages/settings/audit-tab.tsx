/** 审计日志（能力域 13 §审计日志，只读 + 筛选）。 */
import { useState } from "react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { useAuditLogs } from "@/hooks/settings";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/format";

/** AuditTab：审计日志列表（倒序），按操作类型筛选。只读，无写操作。 */
export function AuditTab() {
  const [action, setAction] = useState("");
  const [exporting, setExporting] = useState(false);
  const { data, isLoading } = useAuditLogs(action ? { action, limit: 100 } : { limit: 100 });

  const resultBadge = (r: string) => {
    if (r === "success") return <Badge variant="default">{r}</Badge>;
    return <Badge variant="destructive">{r}</Badge>;
  };

  // handleExport 导出当前筛选下的审计日志为 CSV。
  // 经 http 客户端取 blob（自动带 JWT），再用 objectURL + 隐藏 <a download> 触发下载；
  // 用完 revokeObjectURL 释放。失败由 http 响应拦截器统一 toast.error，此处不重复提示。
  const handleExport = async () => {
    setExporting(true);
    try {
      const { blob, truncated, filename } = await api.exportAuditLogs(action ? { action } : undefined);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
      if (truncated) {
        toast.warning("仅导出前 5 万行，请缩小时间范围");
      } else {
        toast.success("已导出审计日志");
      }
    } catch {
      // 错误提示由 http 响应拦截器统一处理
    } finally {
      setExporting(false);
    }
  };

  return (
    <div className="space-y-3">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="max-w-prose text-sm text-muted-foreground">
          敏感操作留痕（角色变更/API Key/登录等）。只读。
        </p>
        <div className="flex items-center gap-2">
          <label className="text-xs text-muted-foreground" htmlFor="audit-action-filter">筛选</label>
          <Select id="audit-action-filter" value={action} onChange={(e) => setAction(e.target.value)} className="w-44">
            <option value="">全部操作</option>
            <option value="role.create">角色创建</option>
            <option value="role.delete">角色删除</option>
            <option value="role.assign">角色授权</option>
            <option value="role.unassign">角色解权</option>
            <option value="apikey.create">API Key 创建</option>
            <option value="apikey.delete">API Key 撤销</option>
            <option value="auth.login">登录</option>
          </Select>
          <Button variant="outline" size="sm" onClick={handleExport} disabled={exporting}>
            {exporting ? "导出中…" : "导出 CSV"}
          </Button>
        </div>
      </div>
      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-32 w-full" />
          ) : !data || data.items.length === 0 ? (
            <EmptyState title="暂无审计日志" description="敏感操作会在此记录。" />
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">时间</th>
                  <th className="p-3">操作者</th>
                  <th className="p-3">操作</th>
                  <th className="p-3">对象</th>
                  <th className="p-3">结果</th>
                  <th className="p-3">IP</th>
                </tr>
              </thead>
              <tbody>
                {data.items.map((log) => (
                  <tr key={log.id} className="border-b last:border-0">
                    <td className="p-3 text-muted-foreground">{formatTime(log.created_at)}</td>
                    <td className="p-3">
                      <span className="font-medium">{log.actor_name || "—"}</span>
                      {log.actor_user_id > 0 && (
                        <span className="ml-1 text-xs text-muted-foreground">#{log.actor_user_id}</span>
                      )}
                    </td>
                    <td className="p-3 font-mono text-xs">{log.action}</td>
                    <td className="p-3 text-muted-foreground">
                      {log.resource_type}
                      {log.resource_name ? ` · ${log.resource_name}` : ""}
                    </td>
                    <td className="p-3">{resultBadge(log.result)}</td>
                    <td className="p-3 font-mono text-xs text-muted-foreground">{log.ip || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
