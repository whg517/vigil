import * as React from "react";
import { cn } from "@/lib/utils";

/**
 * EmptyState —— 空数据占位（shadcn/ui 风格）。
 * 列表无数据时给出明确指引，比单纯"空白"更友好。
 */
interface EmptyStateProps extends React.HTMLAttributes<HTMLDivElement> {
  icon?: React.ReactNode;
  title: string;
  description?: string;
  action?: React.ReactNode;
}

function EmptyState({
  className,
  icon,
  title,
  description,
  action,
  ...props
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-2 px-6 py-16 text-center",
        className,
      )}
      {...props}
    >
      {icon ? (
        <div className="text-muted-foreground/60">{icon}</div>
      ) : null}
      <p className="text-sm font-medium">{title}</p>
      {description ? (
        <p className="text-xs text-muted-foreground">{description}</p>
      ) : null}
      {action}
    </div>
  );
}

export { EmptyState };
