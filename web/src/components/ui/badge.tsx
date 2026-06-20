import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

/**
 * Badge —— 小标签（shadcn/ui 风格）。
 * 额外提供 severity / status 两组业务 variant，直接映射 index.css 的色板 token，
 * 用于告警严重度与事件状态展示。
 */
const badgeVariants = cva(
  "inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium transition-colors whitespace-nowrap",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground",
        secondary: "bg-secondary text-secondary-foreground",
        outline: "border text-foreground",
        destructive: "bg-destructive text-destructive-foreground",
        // 严重度
        critical: "bg-severity-critical text-severity-critical-foreground",
        warning: "bg-severity-warning text-severity-warning-foreground",
        info: "bg-severity-info text-severity-info-foreground",
        // 事件状态
        triggered: "bg-status-triggered text-status-triggered-foreground",
        escalated: "bg-status-escalated text-status-escalated-foreground",
        acked: "bg-status-acked text-status-acked-foreground",
        resolved: "bg-status-resolved text-status-resolved-foreground",
        closed: "bg-status-closed text-status-closed-foreground",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  },
);

export interface BadgeProps
  extends React.HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}

function Badge({ className, variant, ...props }: BadgeProps) {
  return (
    <span className={cn(badgeVariants({ variant }), className)} {...props} />
  );
}

export { Badge, badgeVariants };
