/**
 * Settings —— 设置页（能力域 13/3/7/8）。
 * 五个 tab：
 *   - RBAC（角色 + 角色绑定 CRUD）
 *   - API Key
 *   - 审计日志
 *   - 通知配置（规则 / 模板 / 抑制规则 CRUD）
 *   - IM 平台状态（飞书/钉钉 Available 只读）
 */
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Tabs } from "@/components/ui/tabs";
import { RBACTab } from "./rbac-tab";
import { APIKeyTab } from "./apikey-tab";
import { AuditTab } from "./audit-tab";
import { NotificationTab } from "./notification-tab";
import { IMTab } from "./im-tab";
import { SubscriptionTab } from "./subscription-tab";

export function Settings() {
  const { t } = useTranslation();
  const [tab, setTab] = useState("rbac");
  return (
    <div className="space-y-4 p-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("settings.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("settings.subtitle")}</p>
      </div>
      <Tabs
        value={tab}
        onValueChange={setTab}
        items={[
          { value: "rbac", label: t("settings.tabRbac") },
          { value: "apikey", label: t("settings.tabApikey") },
          { value: "audit", label: t("settings.tabAudit") },
          { value: "notification", label: t("settings.tabNotification") },
          { value: "subscription", label: t("settings.tabSubscription") },
          { value: "im", label: t("settings.tabIm") },
        ]}
      />
      {tab === "rbac" && <RBACTab />}
      {tab === "apikey" && <APIKeyTab />}
      {tab === "audit" && <AuditTab />}
      {tab === "notification" && <NotificationTab />}
      {tab === "subscription" && <SubscriptionTab />}
      {tab === "im" && <IMTab />}
    </div>
  );
}
