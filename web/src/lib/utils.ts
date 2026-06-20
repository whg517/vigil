import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/**
 * cn —— shadcn/ui 组件的标准 className 合并工具。
 * 合并 clsx（条件类名）与 tailwind-merge（去重冲突的 Tailwind 类）。
 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
