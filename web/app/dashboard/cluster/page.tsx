"use client";

import { useEffect, useState } from "react";
import { API_BASE } from "@/lib/api";

type ClusterStatus = {
  shards: number;
  shard_urls: string[];
  raft_addr?: string;
  description?: string;
};

export default function ClusterPage() {
  const [status, setStatus] = useState<ClusterStatus | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    fetch(`${API_BASE}/api/cluster/status`, { credentials: "include" })
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.json();
      })
      .then(setStatus)
      .catch((e) => setError(String(e)));
  }, []);

  return (
    <div className="space-y-6 p-6">
      <h1 className="text-2xl font-semibold">集群状态</h1>
      <p className="text-sm text-muted-foreground">
        分片路由由 Gateway 环境变量 DATA_SHARD_URLS 配置；Raft 教学节点见 cmd/raft。
      </p>
      {error && <p className="text-red-500">{error}</p>}
      {status && (
        <div className="rounded border p-4 space-y-2">
          <div>分片数: {status.shards}</div>
          <div>说明: {status.description}</div>
          {status.shard_urls?.length > 0 && (
            <ul className="list-disc pl-6">
              {status.shard_urls.map((u) => (
                <li key={u} className="font-mono text-sm">
                  {u}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
