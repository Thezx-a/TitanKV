import { LiveMetrics } from "@/components/live-metrics";
import { MetricCard } from "@/components/metrics-card";
import { API_BASE } from "@/lib/api";
import type { Metrics } from "@/lib/types";

/**
 * Dashboard 首页（React Server Component）。
 * 服务端直接 fetch /api/observability/metrics 拿首屏指标，再交给客户端 LiveMetrics 订阅 SSE 实时更新。
 *
 * 注意：服务端 fetch 不携带浏览器 cookie。Phase 6 骨架阶段后端鉴权由 Gateway 中间件负责，
 * 这里 fetch 失败时返回兜底零值，确保页面可渲染、LiveMetrics 仍会尝试建立 SSE 连接。
 */
async function fetchInitialMetrics(): Promise<Metrics> {
  const fallback: Metrics = {
    qps: 0,
    p50_ms: 0,
    p99_ms: 0,
    storage_gb: 0,
    storage_known: false,
    node_count: 0,
    leader_count: 0,
    timestamp: Math.floor(Date.now() / 1000),
    qps_source: "none",
    latency_approx: false,
    data_backend: "unknown",
  };

  try {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), 3000);
    const res = await fetch(`${API_BASE}/api/observability/metrics`, {
      signal: ctrl.signal,
      // 服务端渲染阶段不缓存，保证指标新鲜
      cache: "no-store",
    });
    clearTimeout(timer);
    if (!res.ok) return fallback;
    const data = (await res.json()) as Partial<Metrics>;
    return { ...fallback, ...data, timestamp: data.timestamp ?? fallback.timestamp };
  } catch {
    return fallback;
  }
}

export default async function DashboardPage() {
  const initial = await fetchInitialMetrics();

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">概览</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          TitanKV 集群实时状态与关键指标。
        </p>
      </div>

      {/* 顶部静态指标：在线节点 / Leader 数 / 采样时间 */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <MetricCard
          title="在线节点"
          value={initial.node_count}
          hint="参与读写的活跃节点数"
        />
        <MetricCard
          title="Leader 数"
          value={initial.leader_count}
          hint="Raft Leader 数量"
        />
        <MetricCard
          title="采样时间"
          value={new Date(initial.timestamp * 1000).toLocaleTimeString("zh-CN")}
          hint="最近一次指标采集"
        />
      </div>

      {/* 实时指标：客户端订阅 SSE */}
      <LiveMetrics initial={initial} />
    </div>
  );
}
