import { cn } from "@/lib/utils";

/**
 * Tabs —— 轻量标签页（无 Radix 依赖）。
 * 用于设置页多分类（IM/RBAC/通知）。
 *
 * 用法：
 *   <Tabs value={tab} onValueChange={setTab} items={[
 *     { value: "im", label: "IM" },
 *     { value: "rbac", label: "RBAC" },
 *   ]} />
 */
interface TabsProps {
  value: string;
  onValueChange: (v: string) => void;
  items: { value: string; label: string }[];
  className?: string;
}

export function Tabs({ value, onValueChange, items, className }: TabsProps) {
  return (
    <div className={cn("flex gap-1 border-b", className)}>
      {items.map((it) => (
        <button
          key={it.value}
          onClick={() => onValueChange(it.value)}
          className={cn(
            "rounded-t-md border-b-2 px-3 py-2 text-sm transition-colors",
            value === it.value
              ? "border-primary font-medium text-foreground"
              : "border-transparent text-muted-foreground hover:text-foreground",
          )}
        >
          {it.label}
        </button>
      ))}
    </div>
  );
}
