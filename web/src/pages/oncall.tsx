/**
 * Oncall —— 值班排班页（能力域 5）。
 * 枚举排班 → 选一个 → 当前在班人 + 未来 N 天预览。
 * 后端：GET /schedules，GET /schedules/:id/oncall，GET /schedules/:id/preview。
 */
import { useState } from "react";
import { CalendarDays, Users } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { useOncall, useSchedulePreview, useSchedules } from "@/hooks/oncall";
import { cn } from "@/lib/utils";

export function Oncall() {
  const { data: schedules, isLoading: loadingSchedules } = useSchedules();
  const [selected, setSelected] = useState<number | undefined>(undefined);
  const [days, setDays] = useState(14);

  const id = selected ?? schedules?.[0]?.id ?? 0;
  const oncall = useOncall(id);
  const preview = useSchedulePreview(id, days);

  return (
    <div className="space-y-4 p-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">值班排班</h1>
        <p className="text-sm text-muted-foreground">
          实时回答"此刻谁在班"。排班是蓝图，值班人由引擎实时计算（不存快照）。
        </p>
      </div>

      {loadingSchedules ? (
        <Skeleton className="h-9 w-64" />
      ) : !schedules || schedules.length === 0 ? (
        <Card>
          <CardContent className="p-6">
            <EmptyState
              icon={<CalendarDays className="h-8 w-8" />}
              title="还没有排班"
              description="创建排班后，告警升级会按排班实时算出在班人。"
            />
          </CardContent>
        </Card>
      ) : (
        <div className="flex items-center gap-3">
          <Select
            value={String(id)}
            onChange={(e) => setSelected(Number(e.target.value))}
            className="w-64"
          >
            {schedules.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}（{s.timezone}）
              </option>
            ))}
          </Select>
          <span className="text-xs text-muted-foreground">预览天数</span>
          <Select
            value={String(days)}
            onChange={(e) => setDays(Number(e.target.value))}
            className="w-24"
          >
            {[7, 14, 30, 60].map((d) => (
              <option key={d} value={d}>
                {d} 天
              </option>
            ))}
          </Select>
        </div>
      )}

      {id > 0 && (
        <div className="grid gap-4 md:grid-cols-2">
          {/* 当前在班人 */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <Users className="h-4 w-4" /> 当前在班人
              </CardTitle>
            </CardHeader>
            <CardContent>
              {oncall.isLoading ? (
                <Skeleton className="h-16 w-full" />
              ) : oncall.isError || !oncall.data?.layers?.length ? (
                <p className="text-sm text-muted-foreground">无在班人或排班未配置。</p>
              ) : (
                <div className="space-y-3">
                  {oncall.data.layers
                    .slice()
                    .sort((a, b) => a.priority - b.priority)
                    .map((layer) => (
                      <div key={layer.name}>
                        <div className="text-xs font-medium text-muted-foreground">
                          {layer.name}（优先级 {layer.priority}）
                        </div>
                        <div className="mt-1 flex flex-wrap gap-2">
                          {layer.users.length === 0 ? (
                            <span className="text-xs text-muted-foreground">—</span>
                          ) : (
                            layer.users.map((u) => (
                              <span
                                key={u.id}
                                className="rounded-md bg-primary/10 px-2 py-1 text-sm font-medium text-primary"
                              >
                                {u.name}
                              </span>
                            ))
                          )}
                        </div>
                      </div>
                    ))}
                </div>
              )}
            </CardContent>
          </Card>

          {/* 预览 */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <CalendarDays className="h-4 w-4" /> 未来 {days} 天预览
              </CardTitle>
            </CardHeader>
            <CardContent>
              {preview.isLoading ? (
                <Skeleton className="h-40 w-full" />
              ) : preview.isError || !preview.data?.days ? (
                <p className="text-sm text-muted-foreground">暂无预览数据。</p>
              ) : (
                <div className="max-h-72 space-y-1 overflow-auto pr-1">
                  {Object.entries(preview.data.days).map(([date, info]) => (
                    <div
                      key={date}
                      className={cn(
                        "flex items-center justify-between rounded-md px-2 py-1.5 text-sm",
                        isToday(date) && "bg-primary/10",
                      )}
                    >
                      <span className="font-mono text-xs text-muted-foreground">{date}</span>
                      <span className="text-xs">
                        {info.users.map((u) => u.name).join("、") || "—"}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  );
}

/** isToday 判断日期串（YYYY-MM-DD 或 RFC3339）是否今天。 */
function isToday(s: string): boolean {
  const today = new Date().toISOString().slice(0, 10);
  return s.startsWith(today);
}
