"use client";

/**
 * Sidebar —— Helicone 版骨架
 * 来源：web/components/ui/sidebar.tsx
 *
 * 提取的关键参数（这些数字就是 Helicone 侧栏"看起来对"的原因）：
 *   展开宽度   12rem  (192px)   ← 比常见的 240px 窄，信息密度更高
 *   移动端     16rem  (256px)
 *   折叠宽度   3rem   (48px)
 *   快捷键     Ctrl/Cmd + B
 *   状态持久   cookie sidebar_state，7 天
 *
 * 注意：Helicone 用自己的 useSidebar() hook 而不是直接用 shadcn 的 context。
 * 这里保留 hook 结构，方便你替换成自己的状态管理。
 */

import * as React from "react";

const SIDEBAR_COOKIE_NAME = "sidebar_state";
const SIDEBAR_COOKIE_MAX_AGE = 60 * 60 * 24 * 7; // 7 天

export const SIDEBAR_WIDTH = "12rem";
export const SIDEBAR_WIDTH_MOBILE = "16rem";
export const SIDEBAR_WIDTH_ICON = "3rem";
export const SIDEBAR_KEYBOARD_SHORTCUT = "b";

type SidebarContextValue = {
  open: boolean;
  setOpen: (open: boolean) => void;
  openMobile: boolean;
  setOpenMobile: (open: boolean) => void;
  isMobile: boolean;
  toggleSidebar: () => void;
};

const SidebarContext = React.createContext<SidebarContextValue | null>(null);

export function useSidebar() {
  const ctx = React.useContext(SidebarContext);
  if (!ctx) {
    throw new Error("useSidebar 必须在 <SidebarProvider> 内部使用");
  }
  return ctx;
}

export const SidebarProvider = React.forwardRef<
  HTMLDivElement,
  React.ComponentProps<"div"> & {
    defaultOpen?: boolean;
    open?: boolean;
    onOpenChange?: (open: boolean) => void;
  }
>(({ defaultOpen = true, open: openProp, onOpenChange, className, children, ...props }, ref) => {
  const [openMobile, setOpenMobile] = React.useState(false);
  const [_open, _setOpen] = React.useState(defaultOpen);
  const open = openProp ?? _open;

  const setOpen = React.useCallback(
    (value: boolean | ((prev: boolean) => boolean)) => {
      const next = typeof value === "function" ? value(open) : value;
      if (onOpenChange) onOpenChange(next);
      else _setOpen(next);

      // 持久化到 cookie
      if (typeof document !== "undefined") {
        document.cookie = `${SIDEBAR_COOKIE_NAME}=${next}; path=/; max-age=${SIDEBAR_COOKIE_MAX_AGE}`;
      }
    },
    [onOpenChange, open]
  );

  const toggleSidebar = React.useCallback(() => {
    setOpen((prev) => !prev);
  }, [setOpen]);

  // Ctrl/Cmd + B 切换侧栏
  React.useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === SIDEBAR_KEYBOARD_SHORTCUT && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        toggleSidebar();
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [toggleSidebar]);

  const value = React.useMemo(
    () => ({ open, setOpen, openMobile, setOpenMobile, isMobile: false, toggleSidebar }),
    [open, setOpen, openMobile, toggleSidebar]
  );

  return (
    <SidebarContext.Provider value={value}>
      <div
        ref={ref}
        className={className}
        style={
          {
            "--sidebar-width": SIDEBAR_WIDTH,
            "--sidebar-width-icon": SIDEBAR_WIDTH_ICON,
          } as React.CSSProperties
        }
        {...props}
      >
        {children}
      </div>
    </SidebarContext.Provider>
  );
});
SidebarProvider.displayName = "SidebarProvider";

/* ---------- 侧栏容器 ---------- */

export const Sidebar = React.forwardRef<
  HTMLDivElement,
  React.ComponentProps<"div"> & { collapsible?: "offcanvas" | "icon" | "none" }
>(({ className, collapsible = "offcanvas", children, ...props }, ref) => {
  const { open, isMobile } = useSidebar();

  return (
    <div
      ref={ref}
      data-state={open ? "expanded" : "collapsed"}
      data-collapsible={collapsible}
      className={[
        "flex flex-col bg-[hsl(var(--sidebar-background))] text-[hsl(var(--sidebar-foreground))]",
        "border-r border-[hsl(var(--sidebar-border))]",
        collapsible === "offcanvas" && !open && !isMobile ? "hidden" : "",
        collapsible === "icon" && !open ? "w-[--sidebar-width-icon]" : "",
        className ?? "",
      ]
        .filter(Boolean)
        .join(" ")}
      style={{ width: open ? SIDEBAR_WIDTH : collapsible === "icon" ? SIDEBAR_WIDTH_ICON : undefined }}
      {...props}
    >
      {children}
    </div>
  );
});
Sidebar.displayName = "Sidebar";

/* ---------- 分组：带标题 ---------- */

export const SidebarGroup = React.forwardRef<HTMLDivElement, React.ComponentProps<"div">>(
  ({ className, ...props }, ref) => (
    <div ref={ref} className={["relative flex w-full min-w-0 flex-col p-2", className].join(" ")} {...props} />
  )
);
SidebarGroup.displayName = "SidebarGroup";

export const SidebarGroupLabel = React.forwardRef<
  HTMLDivElement,
  React.ComponentProps<"div">
>(({ className, ...props }, ref) => (
  <div
    ref={ref}
    className={[
      // Helicone 的分组标签：小写、小字、字距拉开、低对比
      "flex h-8 shrink-0 items-center rounded-md px-2 text-[11px] font-semibold uppercase tracking-wider",
      "text-[hsl(var(--muted-foreground))]",
      className ?? "",
    ].join(" ")}
    {...props}
  />
));
SidebarGroupLabel.displayName = "SidebarGroupLabel";

export const SidebarGroupContent = React.forwardRef<HTMLDivElement, React.ComponentProps<"div">>(
  ({ className, ...props }, ref) => (
    <div ref={ref} className={["w-full text-sm", className].join(" ")} {...props} />
  )
);
SidebarGroupContent.displayName = "SidebarGroupContent";

/* ---------- 菜单项 ---------- */

export const SidebarMenu = React.forwardRef<HTMLUListElement, React.ComponentProps<"ul">>(
  ({ className, ...props }, ref) => (
    <ul ref={ref} className={["flex w-full min-w-0 flex-col gap-1", className].join(" ")} {...props} />
  )
);
SidebarMenu.displayName = "SidebarMenu";

export const SidebarMenuItem = React.forwardRef<HTMLLIElement, React.ComponentProps<"li">>(
  ({ className, ...props }, ref) => (
    <li ref={ref} className={["group/menu-item relative", className].join(" ")} {...props} />
  )
);
SidebarMenuItem.displayName = "SidebarMenuItem";

export const SidebarMenuButton = React.forwardRef<
  HTMLButtonElement,
  React.ComponentProps<"button"> & { isActive?: boolean; asChild?: boolean }
>(({ className, isActive = false, asChild = false, ...props }, ref) => {
  const Comp = asChild ? "a" : "button";
  return (
    <Comp
      ref={ref}
      data-active={isActive}
      className={[
        "flex w-full items-center gap-2 overflow-hidden rounded-md p-2 text-left text-sm",
        "outline-none transition-[color,background-color,box-shadow]",
        "hover:bg-[hsl(var(--sidebar-accent))] hover:text-[hsl(var(--sidebar-accent-foreground))]",
        "focus-visible:ring-2 focus-visible:ring-[hsl(var(--sidebar-ring))]",
        "disabled:pointer-events-none disabled:opacity-50",
        "data-[active=true]:bg-[hsl(var(--sidebar-accent))] data-[active=true]:font-medium data-[active=true]:text-[hsl(var(--sidebar-accent-foreground))]",
        "[&>svg]:size-4 [&>svg]:shrink-0",
        className ?? "",
      ].join(" ")}
      {...props}
    />
  );
});
SidebarMenuButton.displayName = "SidebarMenuButton";

export const SidebarMenuBadge = React.forwardRef<HTMLDivElement, React.ComponentProps<"div">>(
  ({ className, ...props }, ref) => (
    <div
      ref={ref}
      className={[
        "pointer-events-none absolute right-2 flex h-5 min-w-5 items-center justify-center rounded-md px-1",
        "text-xs font-medium tabular-nums",
        "text-[hsl(var(--sidebar-foreground))]",
        className ?? "",
      ].join(" ")}
      {...props}
    />
  )
);
SidebarMenuBadge.displayName = "SidebarMenuBadge";

/* ---------- 底部 ---------- */

export const SidebarFooter = React.forwardRef<HTMLDivElement, React.ComponentProps<"div">>(
  ({ className, ...props }, ref) => (
    <div ref={ref} className={["flex flex-col gap-1 p-2", className].join(" ")} {...props} />
  )
);
SidebarFooter.displayName = "SidebarFooter";

export const SidebarInset = React.forwardRef<HTMLDivElement, React.ComponentProps<"div">>(
  ({ className, ...props }, ref) => (
    <main
      ref={ref}
      className={[
        "relative flex min-h-svh w-full flex-1 flex-col bg-[hsl(var(--background))]",
        "text-[hsl(var(--foreground))]",
        className ?? "",
      ].join(" ")}
      {...props}
    />
  )
);
SidebarInset.displayName = "SidebarInset";

export const SidebarTrigger = React.forwardRef<
  HTMLButtonElement,
  React.ComponentProps<"button">
>(({ className, onClick, ...props }, ref) => {
  const { toggleSidebar } = useSidebar();
  return (
    <button
      ref={ref}
      data-sidebar="trigger"
      onClick={(e) => {
        onClick?.(e);
        toggleSidebar();
      }}
      className={[
        "inline-flex h-7 w-7 items-center justify-center rounded-md",
        "text-[hsl(var(--muted-foreground))] hover:bg-[hsl(var(--accent))]",
        className ?? "",
      ].join(" ")}
      {...props}
    >
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" className="h-4 w-4">
        <rect x="3" y="3" width="18" height="18" rx="2" />
        <path d="M9 3v18" />
      </svg>
      <span className="sr-only">切换侧栏 (Ctrl+B)</span>
    </button>
  );
});
SidebarTrigger.displayName = "SidebarTrigger";

export { SidebarMenuItem as SidebarMenuAction };
