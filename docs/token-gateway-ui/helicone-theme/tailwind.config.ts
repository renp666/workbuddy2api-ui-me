/**
 * Helicone Tailwind 配置 —— 完整提取
 * 来源：github.com/Helicone/helicone → web/tailwind.config.ts
 *
 * 用法：替换你项目根目录的 tailwind.config.ts
 * 如果你不用 TypeScript，把 import/ satisfies 那两行去掉即可。
 */
import type { Config } from "tailwindcss";
import plugin from "tailwindcss/plugin";

export default {
  // Helicone 用 class 策略切换深色模式（不是 media）
  darkMode: ["class"],
  content: [
    "./app/**/*.{ts,tsx,js,jsx}",
    "./pages/**/*.{js,ts,jsx,tsx}",
    "./components/**/*.{js,ts,jsx,tsx}",
    "./src/**/*.{js,ts,jsx,tsx}",
    "./node_modules/@tremor/**/*.{js,ts,jsx,tsx}", // Tremor 图表模块
  ],
  prefix: "",
  theme: {
    container: {
      center: true,
      padding: "2rem",
      screens: { "2xl": "1400px" },
    },
    transparent: "transparent",
    current: "currentColor",
    extend: {
      animation: {
        blink: "blink 1s step-end infinite",
        popin: "popin 1s ease-out forwards",
        "accordion-down": "accordion-down 0.2s ease-out",
        "accordion-up": "accordion-up 0.2s ease-out",
        shine: "shine 2s linear infinite",
        "fade-in": "fadeIn 0.5s ease-out forwards",
      },
      keyframes: {
        popin: {
          "0%": { transform: "scale(0)", opacity: "0" },
          "100%": { transform: "scale(1)", opacity: "1" },
        },
        blink: {
          "0%, 100%": { opacity: "1" },
          "50%": { opacity: "0" },
        },
        shine: {
          "0%": { backgroundPosition: "200% center" },
          "100%": { backgroundPosition: "0% center" },
        },
        "accordion-down": {
          from: { height: "0" },
          to: { height: "var(--radix-accordion-content-height)" },
        },
        "accordion-up": {
          from: { height: "var(--radix-accordion-content-height)" },
          to: { height: "0" },
        },
        fadeIn: {
          "0%": { opacity: "0" },
          "100%": { opacity: "1" },
        },
      },

      colors: {
        // Helicone 品牌色
        heliblue: "#0DA5E8",
        "heliblue-light": "#E6F6FD",
        "heliblue-dark": "#0369A1",

        // 语义化主色（指向 CSS 变量）
        background: {
          DEFAULT: "hsl(var(--background))",
          foreground: "hsl(var(--foreground))",
        },
        foreground: "hsl(var(--foreground))",
        card: {
          DEFAULT: "hsl(var(--card))",
          foreground: "hsl(var(--card-foreground))",
        },
        popover: {
          DEFAULT: "hsl(var(--popover))",
          foreground: "hsl(var(--popover-foreground))",
        },
        primary: {
          DEFAULT: "hsl(var(--primary))",
          foreground: "hsl(var(--primary-foreground))",
        },
        brand: {
          DEFAULT: "hsl(var(--primary))",
          foreground: "hsl(var(--primary-foreground))",
        },
        secondary: {
          DEFAULT: "hsl(var(--secondary))",
          foreground: "hsl(var(--secondary-foreground))",
        },
        muted: {
          DEFAULT: "hsl(var(--muted))",
          foreground: "hsl(var(--muted-foreground))",
        },
        // Helicone 特有：subdued 用于三级信息，比 muted 更亮
        subdued: {
          DEFAULT: "hsl(var(--subdued))",
          foreground: "hsl(var(--subdued-foreground))",
        },
        accent: {
          DEFAULT: "hsl(var(--accent))",
          foreground: "hsl(var(--accent-foreground))",
        },
        destructive: {
          DEFAULT: "hsl(var(--destructive))",
          foreground: "hsl(var(--destructive-foreground))",
        },
        // Helicone 特有：确认态（成功/健康）
        confirmative: {
          DEFAULT: "hsl(var(--confirmative))",
          foreground: "hsl(var(--confirmative-foreground))",
        },
        border: "hsl(var(--border))",
        input: "hsl(var(--input))",
        ring: "hsl(var(--ring))",

        // 图表 10 色
        chart: {
          1: "oklch(var(--chart-1))",
          2: "oklch(var(--chart-2))",
          3: "oklch(var(--chart-3))",
          4: "oklch(var(--chart-4))",
          5: "oklch(var(--chart-5))",
          6: "oklch(var(--chart-6))",
          7: "oklch(var(--chart-7))",
          8: "oklch(var(--chart-8))",
          9: "oklch(var(--chart-9))",
          10: "oklch(var(--chart-10))",
        },

        // 侧栏
        sidebar: {
          background: "var(--sidebar-background)",
          foreground: "hsl(var(--sidebar-foreground))",
          primary: "hsl(var(--sidebar-primary))",
          "primary-foreground": "hsl(var(--sidebar-primary-foreground))",
          accent: "hsl(var(--sidebar-accent))",
          "accent-foreground": "hsl(var(--sidebar-accent-foreground))",
          border: "hsl(var(--sidebar-border))",
          ring: "hsl(var(--sidebar-ring))",
        },

        // ---- Tremor 浅色色板（图表组件内部使用）----
        tremor: {
          brand: {
            faint: "#f0f9ff",   // sky-50
            muted: "#bae6fd",   // sky-200
            subtle: "#38bdf8",  // sky-400
            DEFAULT: "#0284c7", // sky-500
            emphasis: "#0369a1",// sky-700
            inverted: "#ffffff",
          },
          background: {
            muted: "#f9fafb",   // gray-50
            subtle: "#f3f4f6",  // gray-100
            DEFAULT: "#ffffff",
            emphasis: "#374151",// gray-700
          },
          border: { DEFAULT: "#d1d5db" },  // gray-300
          ring: { DEFAULT: "#d1d5db" },
          content: {
            subtle: "#9ca3af",   // gray-400
            DEFAULT: "#6b7280",  // gray-500
            emphasis: "#374151", // gray-700
            strong: "#111827",   // gray-900
            inverted: "#ffffff",
          },
        },

        // ---- Tremor 深色色板 ----
        "dark-tremor": {
          brand: {
            faint: "#ffffff",   // 自定义
            muted: "#0369a1",   // blue-950
            subtle: "#0ea5e9",  // blue-800
            DEFAULT: "#0284c7", // blue-500
            emphasis: "#38bdf8",// blue-400
            inverted: "#f0f9ff",
          },
          background: {
            muted: "#131A2B",   // 自定义
            subtle: "#1f2937",  // gray-800
            DEFAULT: "#000000", // gray-900 -> 纯黑
            emphasis: "#d1d5db",// gray-300
          },
          border: { DEFAULT: "#1f2937" },
          ring: { DEFAULT: "#1f2937" },
          content: {
            subtle: "#4b5563",  // gray-600
            DEFAULT: "#6b7280", // gray-500
            emphasis: "#e5e7eb",// gray-200
            strong: "#f9fafb",  // gray-50
            inverted: "#000000",
          },
        },
      },

      // 圆角：Helicone 用 0.5rem 基准，比 shadcn 默认紧凑
      borderRadius: {
        lg: "var(--radius)",
        md: "calc(var(--radius) - 2px)",
        sm: "calc(var(--radius) - 4px)",
        "tremor-small": "0.375rem",
        "tremor-default": "0.5rem",
        "tremor-full": "9999px",
      },

      // 阴影：浅色深色同值（低透明），靠边框而非阴影做层次
      boxShadow: {
        "tremor-input": "0 1px 2px 0 rgb(0 0 0 / 0.05)",
        "tremor-card":
          "0 1px 3px 0 rgb(0 0 0 / 0.1), 0 1px 2px -1px rgb(0 0 0 / 0.1)",
        "tremor-dropdown":
          "0 4px 6px -1px rgb(0 0 0 / 0.1), 0 2px 4px -2px rgb(0 0 0 / 0.1)",
        "dark-tremor-input": "0 1px 2px 0 rgb(0 0 0 / 0.05)",
        "dark-tremor-card":
          "0 1px 3px 0 rgb(0 0 0 / 0.1), 0 1px 2px -1px rgb(0 0 0 / 0.1)",
        "dark-tremor-dropdown":
          "0 4px 6px -1px rgb(0 0 0 / 0.1), 0 2px 4px -2px rgb(0 0 0 / 0.1)",
      },

      fontSize: {
        "tremor-default": ["0.875rem", { lineHeight: "1.25rem" }],
        "tremor-title": ["1.125rem", { lineHeight: "1.75rem" }],
        "tremor-metric": ["1.875rem", { lineHeight: "2.25rem" }],
      },
    },
  },

  // 动态拼接的 class 需要 safelist，否则生产构建会被 tree-shake 掉
  safelist: [
    {
      pattern:
        /^(bg|text|border|ring|stroke|fill)-(slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-(50|100|200|300|400|500|600|700|800|900|950)$/,
      variants: ["hover", "ui-selected"],
    },
    {
      pattern: /^(bg|text|fill|stroke)-chart-([1-9]|10)$/,
      variants: ["hover"],
    },
  ],

  plugins: [
    require("tailwind-scrollbar"),
    require("tailwindcss-animate"),
    require("@tailwindcss/typography"),
    // Helicone 自定义：支持 group-one ~ group-four 嵌套 hover
    plugin(function ({ addVariant }) {
      addVariant("group-one-hover", ":merge(.group-one):hover &");
      addVariant("group-one-active", ":merge(.group-one):active &");
      addVariant("group-two-hover", ":merge(.group-two):hover &");
      addVariant("group-two-active", ":merge(.group-two):active &");
      addVariant("group-three-hover", ":merge(.group-three):hover &");
      addVariant("group-three-active", ":merge(.group-three):active &");
      addVariant("group-four-hover", ":merge(.group-four):hover &");
      addVariant("group-four-active", ":merge(.group-four):active &");
    }),
  ],
} satisfies Config;
