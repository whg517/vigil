/** IM 平台状态（只读）—— IMTab 展示各 IM 平台适配器的实时就绪状态。 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { api } from "@/lib/api";

/** 平台静态元数据（凭证提示 + 能力矩阵），label/capabilities 走 i18n key 由组件内解析，env 为环境变量原文。 */
const IM_PLATFORM_META: Record<string, { labelKey: string; env: string; capabilitiesKey: string }> = {
  feishu: {
    labelKey: "settings.im.platformFeishu",
    env: "VIGIL_IM_FEISHU_APP_ID/APP_SECRET",
    capabilitiesKey: "settings.im.capabilitiesFeishu",
  },
  dingtalk: {
    labelKey: "settings.im.platformDingtalk",
    env: "VIGIL_IM_DINGTALK_APP_KEY/APP_SECRET",
    capabilitiesKey: "settings.im.capabilitiesDingtalk",
  },
};

/** IMTab 展示各 IM 平台适配器的实时就绪状态（GET /im/platforms）。凭证敏感，仅显示是否就绪。 */
export function IMTab() {
  const { t } = useTranslation();
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
              t("settings.im.loadingStatus")
            ) : isError ? (
              <span className="text-destructive">{t("settings.im.loadError")}</span>
            ) : (
              <>
                {t("settings.im.overviewPrefix", { total: platforms.length })}
                <span className="font-medium text-foreground">{readyCount}</span>
                {t("settings.im.overviewSuffix")}
              </>
            )}
          </div>
          <Button
            size="sm"
            variant="outline"
            onClick={() => qc.invalidateQueries({ queryKey: ["im-platforms"] })}
          >
            {t("settings.im.refresh")}
          </Button>
        </CardContent>
      </Card>

      <div className="grid gap-3 md:grid-cols-2">
        {platforms.map((p) => {
          const metaEntry = IM_PLATFORM_META[p.platform];
          const label = metaEntry ? t(metaEntry.labelKey) : p.platform;
          const env = metaEntry ? metaEntry.env : "—";
          const capabilities = metaEntry ? t(metaEntry.capabilitiesKey) : "—";
          const ready = p.available;
          return (
            <Card key={p.platform}>
              <CardHeader className="flex-row items-center justify-between space-y-0">
                <CardTitle className="text-base">{label}</CardTitle>
                <Badge variant={ready ? "default" : "secondary"}>
                  {ready ? t("settings.im.statusReady") : t("settings.im.statusNotConfigured")}
                </Badge>
              </CardHeader>
              <CardContent className="space-y-2 text-sm text-muted-foreground">
                <p>
                  {t("settings.im.envHintPrefix")}{" "}
                  <code className="rounded bg-muted px-1">{env}</code>{" "}
                  {t("settings.im.envHintSuffix")}
                </p>
                <p>{t("settings.im.capabilitiesLabel", { capabilities })}</p>
                {!ready && (
                  <p className="text-xs text-muted-foreground">
                    {t("settings.im.restartHint")}
                  </p>
                )}
              </CardContent>
            </Card>
          );
        })}
        {platforms.length === 0 && !isLoading && !isError && (
          <Card>
            <CardContent className="p-6">
              <EmptyState
                title={t("settings.im.emptyTitle")}
                description={t("settings.im.emptyDescription")}
              />
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}
