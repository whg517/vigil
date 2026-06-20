import { cn } from "@/lib/utils";

/**
 * Skeleton —— 加载占位骨架（shadcn/ui 风格）。
 * 配合 react-query 的 isLoading 展示，避免数据未到时内容跳动。
 */
function Skeleton({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("animate-pulse rounded-md bg-muted", className)}
      {...props}
    />
  );
}

export { Skeleton };
