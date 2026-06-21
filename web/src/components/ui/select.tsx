import * as React from "react";
import { cn } from "@/lib/utils";

/**
 * Select —— 原生 select 包装（shadcn/ui 风格，无 Radix 依赖）。
 * 用于表单下拉（类型/状态/严重度选择）。
 *
 * 用法：
 *   <Select value={v} onChange={...}>
 *     <option value="a">A</option>
 *   </Select>
 */
const Select = React.forwardRef<
  HTMLSelectElement,
  React.SelectHTMLAttributes<HTMLSelectElement>
>(({ className, children, ...props }, ref) => {
  return (
    <select
      ref={ref}
      className={cn(
        "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    >
      {children}
    </select>
  );
});
Select.displayName = "Select";

export { Select };
