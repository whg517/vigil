/** IM 平台状态（只读）—— IMTab 展示各 IM 平台适配器的实时就绪状态。 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { api } from "@/lib/api";

/** 平台静态元数据（凭证提示 + 能力矩阵），与状态数据合并展示。 */
const IM_PLATFORM_META: Record<string, { label: string; env: string; capabilities: string }> = {
  feishu: {
    label: "飞书（Feishu）",
    env: "VIGIL_IM_FEISHU_APP_ID/APP_SECRET",
    capabilities: "交互卡片✅ 卡片更新✅ 建群✅ @人✅ 命令机器人✅",
  },
  dingtalk: {
    label: "钉钉（DingTalk）",
    env: "VIGIL_IM_DINGTALK_APP_KEY/APP_SECRET",
    capabilities: "交互卡片✅ 卡片更新⚠️（降级发新消息）建群✅ @人✅ 命令机器人✅",
  },
  wecom: {
    label: "企业微信（WeCom）",
    env: "（待 PoC）",
    capabilities: "占位适配器（NoopBot），未接入真实 API",
  },
};

/** IMTab 展示各 IM 平台适配器的实时就绪状态（GET /im/platforms）。凭证敏感，仅显示是否就绪。 */
export function IMTab() {
  const qc = useQueryClient();
  const { data, isLoading, isError } = useQuery({
    queryKey: ["im-platforms"],
    queryFn: () => api.listIMPlatforms(),
    // 后端无凭证平台返回 available=false，状态稳定，低频刷新即可
    staleTime: 60_000,
  });

  const platforms = data ?? [];
  const readyCount = platforms.filter((p) => p.available).length;

  return (
    <div className="space-y-4">
      {/* 就绪总览 */}
      <Card>
        <CardContent className="flex flex-wrap items-center justify-between gap-3 p-4">
          <div className="text-sm text-muted-foreground">
            {isLoading ? (
              "加载平台状态…"
            ) : isError ? (
              <span className="text-destructive">平台状态查询失败，请检查后端 /im/platforms 接口。</span>
            ) : (
              <>
                共 {platforms.length} 个平台，<span className="font-medium text-foreground">{readyCount}</span> 个已就绪。
                凭证经环境变量配置，此处只读展示状态。
              </>
            )}
          </div>
          <Button
            size="sm"
            variant="outline"
            onClick={() => qc.invalidateQueries({ queryKey: ["im-platforms"] })}
          >
            刷新
          </Button>
        </CardContent>
      </Card>

      <div className="grid gap-3 md:grid-cols-2">
        {platforms.map((p) => {
          const meta = IM_PLATFORM_META[p.platform] ?? { label: p.platform, env: "—", capabilities: "—" };
          const ready = p.available;
          return (
            <Card key={p.platform}>
              <CardHeader className="flex-row items-center justify-between space-y-0">
                <CardTitle className="text-base">{meta.label}</CardTitle>
                <Badge variant={ready ? "default" : "secondary"}>
                  {ready ? "已就绪" : p.impl === "noop" ? "未接入" : "未配置"}
                </Badge>
              </CardHeader>
              <CardContent className="space-y-2 text-sm text-muted-foreground">
                <p>
                  配置环境变量 <code className="rounded bg-muted px-1">{meta.env}</code> 启用。
                </p>
                <p>能力：{meta.capabilities}</p>
                {!ready && p.impl !== "noop" && (
                  <p className="text-xs text-muted-foreground">
                    提示：修改 .env 后重启后端生效。
                  </p>
                )}
              </CardContent>
            </Card>
          );
        })}
        {platforms.length === 0 && !isLoading && !isError && (
          <Card>
            <CardContent className="p-6">
              <EmptyState title="无 IM 平台" description="后端未注册任何 IM 适配器。" />
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}
