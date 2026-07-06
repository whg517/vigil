/**
 * 集成向导（能力域 14 M14.6）—— 把"新接入一个告警源"从手填表单升级为分步向导，降低 onboarding 门槛。
 *
 * 四步：
 *   1. 选类型   —— 列出支持的接入类型（调 config-template 拿全集），每种带中文名 + 简介。
 *   2. 配置     —— 据所选 type 调 config-template 拿字段说明 + 接线指引，渲染配置表单 + 上游怎么配。
 *   3. 生成接入 —— 创建 Integration，展示 webhook URL / token（明文仅此一次）+ 源端配置片段。
 *   4. 验证     —— 干跑测试（POST /integrations/:id/test），展示归一化预览（labels/severity），失败给排查提示。
 *
 * 分步 state（step 1→4）：每步校验后进下一步，可返回上一步。第 3 步创建成功后不可回退（token 已生成、仅显示一次）。
 */
import { useMemo, useState } from "react";
import { ArrowLeft, ArrowRight, Check, Copy, Loader2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { useConfigTemplates, useCreateIntegration, useTestIntegration } from "@/hooks/integrations";
import { toast } from "sonner";
import type {
  IntegrationConfigTemplate,
  IntegrationCreated,
  IntegrationTestResult,
} from "@/lib/types";

/** 向导步骤枚举（1 选类型 → 2 配置 → 3 生成 → 4 验证）。 */
type WizardStep = 1 | 2 | 3 | 4;

const STEP_LABELS: Record<WizardStep, string> = {
  1: "选类型",
  2: "配置",
  3: "生成接入信息",
  4: "验证",
};

/** 各类型的示例干跑 payload（step4 预填，方便一键验证；用户可改）。 */
const SAMPLE_PAYLOADS: Record<string, string> = {
  prometheus: JSON.stringify(
    {
      alerts: [
        {
          status: "firing",
          labels: { alertname: "HighCPU", severity: "critical", service: "api", env: "prod" },
          annotations: { summary: "CPU 使用率过高" },
        },
      ],
    },
    null,
    2,
  ),
  webhook: JSON.stringify(
    { source_event_id: "evt-1", severity: "critical", summary: "磁盘将满", labels: { env: "prod", service: "api" } },
    null,
    2,
  ),
  api: JSON.stringify(
    { source_event_id: "evt-1", severity: "warning", summary: "示例告警", labels: { env: "staging" } },
    null,
    2,
  ),
};

/** 复制到剪贴板 + toast 反馈（沿用 integrations.tsx 现有模式）。 */
async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text);
    toast.success("已复制");
  } catch {
    toast.error("复制失败");
  }
}

/** 顶部步骤条：高亮当前步、已完成步打勾。 */
function StepIndicator({ step }: { step: WizardStep }) {
  const steps: WizardStep[] = [1, 2, 3, 4];
  return (
    <div className="mb-4 flex items-center gap-1">
      {steps.map((s, i) => {
        const done = s < step;
        const active = s === step;
        return (
          <div key={s} className="flex flex-1 items-center gap-1">
            <div className="flex items-center gap-1.5">
              <span
                className={
                  "flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-xs font-medium " +
                  (active
                    ? "bg-primary text-primary-foreground"
                    : done
                      ? "bg-primary/20 text-primary"
                      : "bg-muted text-muted-foreground")
                }
              >
                {done ? <Check className="h-3.5 w-3.5" /> : s}
              </span>
              <span className={"text-xs " + (active ? "font-medium text-foreground" : "text-muted-foreground")}>
                {STEP_LABELS[s]}
              </span>
            </div>
            {i < steps.length - 1 && <div className="mx-1 h-px flex-1 bg-border" />}
          </div>
        );
      })}
    </div>
  );
}

/** 集成向导对话框。onClose 关闭（未创建=纯取消；已创建=接入点已落库，父页应刷新列表）。 */
export function IntegrationWizard({ onClose }: { onClose: () => void }) {
  const [step, setStep] = useState<WizardStep>(1);
  const { data: templates, isLoading } = useConfigTemplates();

  // step1 所选类型
  const [type, setType] = useState<string>("");
  // step2 配置：名称 + 动态 config 字段值
  const [name, setName] = useState("");
  const [configValues, setConfigValues] = useState<Record<string, string>>({});
  // step3 创建结果（含一次性 token）
  const [created, setCreated] = useState<IntegrationCreated | null>(null);
  // step4 干跑结果
  const [testResult, setTestResult] = useState<IntegrationTestResult | null>(null);
  const [samplePayload, setSamplePayload] = useState("");

  const create = useCreateIntegration();
  const test = useTestIntegration();

  const selected: IntegrationConfigTemplate | undefined = useMemo(
    () => templates?.find((t) => t.type === type),
    [templates, type],
  );

  // 进入 step2：选定 type 后带上其示例 payload（供 step4 预填），名称给个建议默认。
  const chooseType = (t: string) => {
    setType(t);
    setName((prev) => prev || t);
    setSamplePayload(SAMPLE_PAYLOADS[t] ?? SAMPLE_PAYLOADS.webhook);
    setConfigValues({});
    setStep(2);
  };

  // step2 → step3：创建接入点。config 只收非空字段（避免写入空串）。
  const submitCreate = () => {
    const config: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(configValues)) {
      if (v !== "") config[k] = v;
    }
    create.mutate(
      { name, type, config: Object.keys(config).length ? config : undefined },
      {
        onSuccess: (data) => {
          setCreated(data);
          setStep(3);
        },
      },
    );
  };

  // step4：干跑测试。payload 解析失败给前端提示，不打后端。
  const runTest = () => {
    if (!created) return;
    let parsed: unknown;
    try {
      parsed = samplePayload.trim() ? JSON.parse(samplePayload) : {};
    } catch {
      toast.error("样例 payload 不是合法 JSON");
      return;
    }
    test.mutate(
      { id: created.id, payload: parsed },
      {
        onSuccess: (res) => setTestResult(res),
        onError: (e: unknown) => {
          const msg = e instanceof Error ? e.message : "测试失败";
          toast.error(`干跑测试失败：${msg}`);
        },
      },
    );
  };

  const webhookUrl = created ? `${window.location.origin}/api/v1/webhook/${created.token}` : "";

  return (
    <Dialog
      open
      onClose={onClose}
      title="新建接入向导"
      description="分步接入告警源：选类型 → 配置 → 生成接入信息 → 验证。"
      className="max-w-2xl"
    >
      <StepIndicator step={step} />

      {/* —— Step 1 选类型 —— */}
      {step === 1 && (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">选择要接入的告警源类型：</p>
          {isLoading ? (
            <div className="space-y-2">
              <Skeleton className="h-16 w-full" />
              <Skeleton className="h-16 w-full" />
            </div>
          ) : (
            <div className="grid max-h-[50vh] gap-2 overflow-y-auto sm:grid-cols-2">
              {(templates ?? []).map((t) => (
                <button
                  key={t.type}
                  onClick={() => chooseType(t.type)}
                  className="rounded-md border p-3 text-left transition-colors hover:border-primary hover:bg-accent"
                >
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{t.display_name}</span>
                    <Badge variant="outline">{t.type}</Badge>
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">{t.description}</p>
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      {/* —— Step 2 配置 —— */}
      {step === 2 && selected && (
        <div className="space-y-4">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">接入点名称</label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={`prod-${selected.type}`}
              autoFocus
            />
          </div>

          {(selected.fields ?? []).map((f) => (
            <div key={f.key} className="space-y-1.5">
              <label className="text-sm font-medium">
                {f.label}
                {f.required && <span className="ml-0.5 text-destructive">*</span>}
              </label>
              <Input
                value={configValues[f.key] ?? ""}
                onChange={(e) => setConfigValues((prev) => ({ ...prev, [f.key]: e.target.value }))}
                placeholder={f.example}
              />
              {f.help && <p className="text-xs text-muted-foreground">{f.help}</p>}
            </div>
          ))}

          {/* 上游怎么配：接线指引 */}
          <div className="rounded-md border bg-muted/50 p-3">
            <p className="mb-1 text-xs font-medium text-foreground">上游如何指向 Vigil</p>
            <p className="whitespace-pre-line text-xs text-muted-foreground">{selected.setup_hint}</p>
          </div>

          <div className="flex justify-between gap-2 pt-1">
            <Button variant="outline" onClick={() => setStep(1)}>
              <ArrowLeft className="mr-1 h-4 w-4" /> 上一步
            </Button>
            <Button
              disabled={!name || create.isPending || missingRequired(selected, configValues)}
              onClick={submitCreate}
            >
              {create.isPending ? (
                <>
                  <Loader2 className="mr-1 h-4 w-4 animate-spin" /> 创建中...
                </>
              ) : (
                <>
                  创建接入点 <ArrowRight className="ml-1 h-4 w-4" />
                </>
              )}
            </Button>
          </div>
        </div>
      )}

      {/* —— Step 3 生成接入信息 —— */}
      {step === 3 && created && (
        <div className="space-y-3">
          <div className="rounded-md border border-amber-500/40 bg-amber-500/10 p-2.5 text-xs text-amber-700 dark:text-amber-400">
            ⚠️ 鉴权 token 仅此一次展示，请立即复制保存；离开本步骤后无法再次查看。
          </div>
          <SecretRow label="Webhook URL（告警源推送到此）" value={webhookUrl} />
          <SecretRow label="鉴权 Token（如需 Header 方式）" value={created.token} />
          {selected?.setup_hint && (
            <div className="rounded-md border bg-muted/50 p-3">
              <p className="mb-1 text-xs font-medium text-foreground">源端配置片段</p>
              <p className="whitespace-pre-line text-xs text-muted-foreground">
                {selected.setup_hint.replaceAll("<vigil-host>", window.location.host).replaceAll("<token>", created.token)}
              </p>
            </div>
          )}
          <div className="flex justify-end pt-1">
            <Button onClick={() => setStep(4)}>
              下一步：验证配置 <ArrowRight className="ml-1 h-4 w-4" />
            </Button>
          </div>
        </div>
      )}

      {/* —— Step 4 验证 —— */}
      {step === 4 && created && (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            用样例 payload 干跑归一化（不建单、不落库），确认 labels 命中、severity 映射正确后再让源端正式推送。
          </p>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">样例 payload（JSON）</label>
            <Textarea
              value={samplePayload}
              onChange={(e) => setSamplePayload(e.target.value)}
              rows={6}
              className="font-mono text-xs"
            />
          </div>
          <Button onClick={runTest} disabled={test.isPending}>
            {test.isPending ? (
              <>
                <Loader2 className="mr-1 h-4 w-4 animate-spin" /> 干跑中...
              </>
            ) : (
              "运行干跑测试"
            )}
          </Button>

          {testResult && <TestResultView result={testResult} />}

          <div className="flex justify-end border-t pt-3">
            <Button variant="outline" onClick={onClose}>
              完成
            </Button>
          </div>
        </div>
      )}
    </Dialog>
  );
}

/** missingRequired 判断是否有必填 config 字段未填（step2 提交前门禁）。 */
function missingRequired(
  tpl: IntegrationConfigTemplate,
  values: Record<string, string>,
): boolean {
  return (tpl.fields ?? []).some((f) => f.required && !(values[f.key] ?? "").trim());
}

/** SecretRow 只读密文行 + 复制按钮（沿用创建成功页的"仅显示一次"模式）。 */
function SecretRow({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <label className="text-sm font-medium">{label}</label>
      <div className="mt-1 flex items-center gap-2 rounded-md border bg-muted p-3">
        <code className="flex-1 break-all text-xs">{value}</code>
        <Button size="sm" variant="outline" onClick={() => copyText(value)}>
          <Copy className="mr-1 h-4 w-4" /> 复制
        </Button>
      </div>
    </div>
  );
}

/** TestResultView 干跑结果展示：成功=归一化预览（severity/labels），失败=排查提示。 */
function TestResultView({ result }: { result: IntegrationTestResult }) {
  if (!result.matched) {
    return (
      <div className="space-y-1.5 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-xs">
        <p className="font-medium text-destructive">归一化失败</p>
        <p className="text-muted-foreground">{result.error || "适配器未能解析该 payload。"}</p>
        <p className="text-muted-foreground">
          排查：确认 payload 结构符合该类型适配器要求（如 Prometheus 需 alerts[]），字段名/嵌套是否匹配；
          可对照上一步的接线指引调整源端配置。
        </p>
      </div>
    );
  }
  return (
    <div className="space-y-2 rounded-md border border-primary/30 bg-primary/5 p-3">
      <p className="text-xs font-medium text-foreground">
        归一化成功 · 产出 {result.count ?? result.events?.length ?? 0} 条 Event 预览
      </p>
      <div className="space-y-2">
        {(result.events ?? []).map((ev, i) => (
          <div key={i} className="rounded border bg-background p-2 text-xs">
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant={severityVariant(ev.severity)}>{ev.severity || "—"}</Badge>
              {ev.status && <Badge variant="outline">{ev.status}</Badge>}
              <span className="font-medium">{ev.summary || ev.source_event_id || "(无摘要)"}</span>
            </div>
            {ev.labels && Object.keys(ev.labels).length > 0 && (
              <div className="mt-1.5 flex flex-wrap gap-1">
                {Object.entries(ev.labels).map(([k, v]) => (
                  <Badge key={k} variant="secondary" className="font-mono">
                    {k}={v}
                  </Badge>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

/** severity 字符串 → Badge variant（归一化后的原始 severity 串，可能超出 critical/warning/info）。 */
function severityVariant(severity?: string): "critical" | "warning" | "info" {
  if (severity === "critical") return "critical";
  if (severity === "warning") return "warning";
  return "info";
}
