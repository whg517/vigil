import { Button } from "@/components/ui/button";

/**
 * Dashboard —— 占位仪表盘页。
 * 验证前端骨架（Tailwind + shadcn 风格组件 + 路由）跑通。
 * 业务指标（告警量/MTTR/团队负载等，见能力域 15）待接入后端 API。
 */
export function Dashboard() {
  return (
    <div className="space-y-6 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">仪表盘</h1>
          <p className="text-sm text-muted-foreground">
            Vigil 告警处置平台 —— 业务指标待接入
          </p>
        </div>
        <Button>新建事件</Button>
      </div>

      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
        {[
          { label: "活跃事件", value: "—" },
          { label: "今日告警", value: "—" },
          { label: "MTTA", value: "—" },
          { label: "MTTR", value: "—" },
        ].map((stat) => (
          <div
            key={stat.label}
            className="rounded-lg border bg-card p-4 shadow-sm"
          >
            <div className="text-sm text-muted-foreground">{stat.label}</div>
            <div className="mt-1 text-2xl font-semibold">{stat.value}</div>
          </div>
        ))}
      </div>
    </div>
  );
}
