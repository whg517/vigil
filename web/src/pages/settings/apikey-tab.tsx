/** API Key（能力域 13 §API Key 管理）—— APIKeyTab：列出/创建/撤销 API Key。 */
import { useState } from "react";
import { Copy, KeyRound, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { useAPIKeys, useCreateAPIKey, useDeleteAPIKey } from "@/hooks/settings";
import { toast } from "sonner";
import { formatTime } from "@/lib/format";
import { Trans, useTranslation } from "react-i18next";

/** APIKeyTab：列出/创建/撤销 API Key。创建时明文 token 仅展示一次，可复制。 */
export function APIKeyTab() {
  const { t } = useTranslation();
  const { data, isLoading } = useAPIKeys();
  const del = useDeleteAPIKey();
  const [creating, setCreating] = useState(false);

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">
          <Trans
            i18nKey="settings.apikey.intro"
            components={{ code: <code className="rounded bg-muted px-1" /> }}
          />
        </p>
        <Button size="sm" onClick={() => setCreating(true)}>
          <KeyRound className="mr-1 h-4 w-4" /> {t("common.create")}
        </Button>
      </div>
      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <Skeleton className="h-20 w-full" />
          ) : !data || data.length === 0 ? (
            <EmptyState title={t("settings.apikey.emptyTitle")} description={t("settings.apikey.emptyDesc")} />
          ) : (
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-3">{t("settings.apikey.colName")}</th>
                  <th className="p-3">{t("settings.apikey.colPrefix")}</th>
                  <th className="p-3">{t("settings.apikey.colStatus")}</th>
                  <th className="p-3">{t("settings.apikey.colLastUsed")}</th>
                  <th className="p-3">{t("settings.apikey.colCreatedAt")}</th>
                  <th className="p-3"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((k) => (
                  <tr key={k.id} className="border-b last:border-0">
                    <td className="p-3 font-medium">{k.name}</td>
                    <td className="p-3 font-mono text-xs text-muted-foreground">{k.prefix}…</td>
                    <td className="p-3">
                      <Badge variant={k.status === "active" ? "default" : "secondary"}>
                        {t(`settings.apikey.status.${k.status}`)}
                      </Badge>
                    </td>
                    <td className="p-3 text-muted-foreground">
                      {k.last_used_at ? formatTime(k.last_used_at) : "—"}
                    </td>
                    <td className="p-3 text-muted-foreground">{formatTime(k.created_at)}</td>
                    <td className="p-3 text-right">
                      <Button
                        size="icon"
                        variant="ghost"
                        disabled={del.isPending}
                        onClick={() => del.mutate(k.id)}
                      >
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
      {creating && <CreateAPIKeyDialog onClose={() => setCreating(false)} />}
    </div>
  );
}

/** CreateAPIKeyDialog 创建表单。成功后展示一次性明文 token + 复制按钮。 */
function CreateAPIKeyDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const create = useCreateAPIKey();
  const [name, setName] = useState("");
  const [expiresIn, setExpiresIn] = useState("");
  const [plaintext, setPlaintext] = useState<string | null>(null);

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const hours = expiresIn ? parseInt(expiresIn, 10) : undefined;
    create.mutate(
      { name, expires_in_hours: hours && hours > 0 ? hours : undefined },
      { onSuccess: (data) => setPlaintext(data.token) },
    );
  };

  const copyToken = async () => {
    if (!plaintext) return;
    try {
      await navigator.clipboard.writeText(plaintext);
      toast.success(t("settings.apikey.copySuccess"));
    } catch {
      toast.error(t("settings.apikey.copyError"));
    }
  };

  // 创建成功后：展示一次性明文 token，不再显示表单
  if (plaintext) {
    return (
      <Dialog open onClose={onClose} title={t("settings.apikey.createdTitle")} description={t("settings.apikey.createdDesc")}>
        <div className="space-y-3">
          <div className="flex items-center gap-2 rounded-md border bg-muted p-3">
            <code className="flex-1 break-all text-xs">{plaintext}</code>
            <Button size="sm" variant="outline" onClick={copyToken}>
              <Copy className="mr-1 h-4 w-4" /> {t("settings.apikey.copy")}
            </Button>
          </div>
          <Button className="w-full" onClick={onClose}>{t("settings.apikey.saved")}</Button>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog open onClose={onClose} title={t("settings.apikey.createTitle")} description={t("settings.apikey.createDesc")}>
      <form className="space-y-3" onSubmit={onSubmit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("settings.apikey.nameLabel")}</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="ci-deploy-key" required autoFocus />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t("settings.apikey.expiresLabel")}</label>
          <Input
            value={expiresIn}
            onChange={(e) => setExpiresIn(e.target.value)}
            placeholder="720"
            type="number"
            min={1}
          />
        </div>
        <Button type="submit" className="w-full" disabled={create.isPending || !name}>
          {create.isPending ? t("common.submitting") : t("common.create")}
        </Button>
      </form>
    </Dialog>
  );
}
