"use client";

import { useEffect, useRef, useState } from "react";
import type { Metrics } from "@/lib/types";
import { API_BASE } from "@/lib/api";
import { MetricCard } from "./metrics-card";

interface LiveMetricsProps {
  /** 首屏 SSR 透传进来的初始数据，避免客户端首帧空白 */
  initial: Metrics;
}

/**
 * 客户端实时指标组件：
 * 通过 EventSource 订阅 `${API_BASE}/api/metrics/stream`（SSE），
 * 每次收到 metrics 事件即覆盖最近一次指标快照；组件卸载时关闭连接。
 *
 * 注意：后端 observability/handler.go 推送的 SSE 带有 `event: metrics`，
 * 因此必须用 addEventListener("metrics", ...) 监听，而不是 onmessage。
 */
export function LiveMetrics({ initial }: LiveMetricsProps) {
  const [metrics, setMetrics] = useState<Metrics>(initial);
  const [connected, setConnected] = useState(false);
  const [lastError, setLastError] = useState<string | null>(null);
  const sourceRef = useRef<EventSource | null>(null);

  useEffect(() => {
    // 跨域请求 Gateway，需后端开启 CORS（开发环境默认放行）
    const url = `${API_BASE}/api/metrics/stream`;
    const source = new EventSource(url);
    sourceRef.current = source;

    source.onopen = () => {
      setConnected(true);
      setLastError(null);
    };

    // 后端推送的 SSE 带有 event: metrics，必须用 addEventListener 监听
    source.addEventListener("metrics", (ev: MessageEvent) => {
      try {
        const data = JSON.parse(ev.data) as Partial<Metrics>;
        setMetrics((prev) => ({ ...prev, ...data }));
      } catch {
        // 忽略非 JSON 帧
      }
    });

    source.onerror = () => {
      setConnected(false);
      setLastError("实时连接中断，正在重试…");
      // EventSource 会自动重连，无需手动 close
    };

    return () => {
      source.close();
      sourceRef.current = null;
    };
  }, []);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold tracking-tight">实时指标</h2>
        <span
          className={
            "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs " +
            (connected
              ? "bg-emerald-100 text-emerald-700"
              : "bg-amber-100 text-amber-700")
          }
        >
          <span
            className={
              "h-1.5 w-1.5 rounded-full " +
              (connected ? "bg-emerald-500" : "bg-amber-500 animate-pulse")
            }
          />
          {connected ? "已连接" : "重连中"}
        </span>
      </div>

      {/* F1: Data backend=memory → 诚实黄条（非生产 LSM） */}
      {metrics.data_backend === "memory" && (
        <div
          role="status"
          className="rounded-md border border-amber-500/50 bg-amber-50 px-3 py-2 text-sm text-amber-900"
        >
          Data 后端为 <code className="font-mono">memory</code>
          ：未接 minikv（缺{" "}
          <code className="font-mono">MINIKV_ADDR</code>
          ）。演示可用，数据不落盘。启动{" "}
          <code className="font-mono">minikv_server</code> 并设置{" "}
          <code className="font-mono">MINIKV_ADDR</code> 后刷新。
        </div>
      )}

      {lastError && !connected && (
        <p className="text-xs text-amber-700">{lastError}</p>
      )}

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <MetricCard
          title="QPS"
          value={metrics.qps.toFixed(1)}
          hint={metrics.qps_source === "prometheus_delta" ? "Δ/Δt 真实刮数" : "等待采样…"}
        />
        <MetricCard
          title={metrics.latency_approx ? "延迟(均值)" : "P50 延迟"}
          value={`${metrics.p50_ms.toFixed(2)} ms`}
          hint={metrics.latency_approx ? "近似，非真分位" : "中位数"}
        />
        <MetricCard
          title="P99 延迟"
          value={metrics.p99_ms > 0 ? `${metrics.p99_ms.toFixed(2)} ms` : "—"}
          hint={metrics.p99_ms > 0 ? "尾部延迟" : "无 histogram"}
        />
        <MetricCard
          title="存储用量"
          value={
            metrics.storage_known
              ? `${metrics.storage_gb.toFixed(2)} GB`
              : "未知"
          }
          hint={`节点 ${metrics.node_count} · leader ${metrics.leader_count}`}
        />
      </div>
    </div>
  );
}
