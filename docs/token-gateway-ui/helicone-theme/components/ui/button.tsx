"use client";

/**
 * Button —— Helicone 版
 * 来源：web/components/ui/button.tsx
 *
 * 与 shadcn 默认版的差异（这是 Helicone 风格的灵魂）：
 *  1. 新增 action / glass / ghostLinear 三个 variant
 *  2. 新增 sleek 系列小尺寸（sm_sleeve / md_sleek），用于表格内操作
 *  3. 新增 square_icon（27px 方形图标按钮，表格行内用）
 *  4. 支持 asPill 圆角胶囊
 *  5. interactive 类实现 hover 升起 + 按下缩放
 */

import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center whitespace-nowrap rounded-md text-sm font-medium ring-offset-white transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[hsl(var(--ring))] focus-visible:ring-offset-2 disabled:pointer-events-none disabled:opacity-50 dark:ring-offset-slate-950 dark:focus-visible:ring-slate-300 select-none",
  {
    variants: {
      variant: {
        default:
          "bg-primary text-primary-foreground shadow hover:bg-primary/90",
        destructive:
          "bg-destructive text-destructive-foreground shadow-sm hover:bg-destructive/90",
        outline:
          "border border-input bg-background shadow-sm hover:bg-accent hover:text-accent-foreground",
        secondary:
          "bg-secondary text-secondary-foreground shadow-sm hover:bg-secondary/80",
        ghost: "hover:bg-accent hover:text-accent-foreground",
        link: "text-primary underline-offset-4 hover:underline",

        /* ---- Helicone 特有 variant ---- */

        // 主操作按钮：带边框 + interactive 缩放反馈
        action:
          "bg-[hsl(var(--primary))] text-white hover:bg-[hsl(var(--primary))]/90 border border-[hsl(var(--border))] interactive",

        // 毛玻璃按钮：用于悬浮在内容之上的场景
        glass:
          "glass border border-slate-200 dark:text-slate-300 dark:border-slate-700 hover:bg-slate-100 hover:text-slate-900 dark:hover:bg-slate-800 dark:hover:text-slate-50",

        // 幽灵按钮的线性变体（弱化版 ghost）
        ghostLinear:
          "hover:bg-[hsl(var(--accent))] hover:text-[hsl(var(--accent-foreground))]",

        none: "",
      },
      size: {
        xs: "h-8 px-3 text-xs",
        default: "h-9 px-4 py-2",
        sm: "h-8 rounded-md px-3 text-xs font-normal",
        lg: "h-10 rounded-md px-8",
        icon: "h-10 w-10",

        /* ---- Helicone 特有尺寸 ---- */

        // 超紧凑，24px 高 —— 表格行内操作按钮
        sm_sleek: "h-6 rounded-md px-3 text-xs",
        // 紧凑，32px 高 —— 工具栏
        md_sleek: "h-8 rounded-md px-3 text-sm",
        // 27px 方形图标按钮 —— 表格最右侧的操作列
        square_icon: "h-7 w-7 p-0",

        none: "",
      },
      asPill: {
        true: "rounded-full",
        false: "",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
      asPill: false,
    },
  }
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
  asPill?: boolean;
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  (
    { className, variant, size, asChild = false, asPill = false, ...props },
    ref
  ) => {
    const Comp = asChild ? Slot : "button";
    return (
      <Comp
        className={cn(buttonVariants({ variant, size, asPill }), className)}
        ref={ref}
        {...props}
      />
    );
  }
);
Button.displayName = "Button";

export { Button, buttonVariants };
