"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { API_BASE } from "@/lib/api";

/**
 * 侧边栏导航组件。使用 usePathname 高亮当前路由。
 * 退出登录按钮调 ${API_BASE}/api/auth/logout 吊销 token，并清除本地 cookie。
 */
interface NavItem {
  href: string;
  label: string;
  badge?: string;
}

const NAV_ITEMS: NavItem[] = [
  { href: "/dashboard", label: "概览", badge: "OV" },
  { href: "/dashboard/data", label: "数据浏览", badge: "KV" },
  { href: "/dashboard/collections", label: "Collection 管理", badge: "CO" },
  { href: "/dashboard/rag", label: "知识库", badge: "RG" },
  { href: "/dashboard/cluster", label: "集群", badge: "CL" },
  { href: "/dashboard/users", label: "用户管理", badge: "US" },
];

export function Nav() {
  const pathname = usePathname();

  async function handleLogout() {
    try {
      await fetch(`${API_BASE}/api/auth/logout`, {
        method: "POST",
        credentials: "include",
      });
    } catch {
      // 忽略网络错误
    }
    document.cookie = "titan_token=; Max-Age=0; path=/";
    window.location.href = "/login";
  }

  return (
    <aside className="flex h-screen w-60 flex-col border-r bg-muted/30">
      <div className="flex h-14 items-center gap-2 border-b px-5">
        <div className="flex h-7 w-7 items-center justify-center rounded bg-primary text-xs font-bold text-primary-foreground">
          TK
        </div>
        <span className="text-sm font-semibold tracking-tight">TitanKV Console</span>
      </div>

      <nav className="flex-1 space-y-1 p-3">
        {NAV_ITEMS.map((item) => {
          const active =
            pathname === item.href ||
            (item.href !== "/dashboard" && pathname.startsWith(item.href));
          return (
            <Link
              key={item.href}
              href={item.href}
              className={
                "flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors " +
                (active
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground")
              }
            >
              <span
                className={
                  "flex h-5 w-5 items-center justify-center rounded text-[10px] font-semibold " +
                  (active
                    ? "bg-primary-foreground/20 text-primary-foreground"
                    : "bg-muted-foreground/15 text-muted-foreground")
                }
              >
                {item.badge}
              </span>
              {item.label}
            </Link>
          );
        })}
      </nav>

      <div className="border-t p-3">
        <button
          type="button"
          onClick={handleLogout}
          className="w-full rounded-md border px-3 py-2 text-left text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          退出登录
        </button>
      </div>
    </aside>
  );
}
