"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { ragApi } from "@/lib/rag";
import { api, HttpError } from "@/lib/api";
import type { Collection, IngestResponse } from "@/lib/types";

/**
 * 知识库详情页：文档列表 + 上传 + 删除 + 入口 chat 调试.
 *
 * - 路由: /dashboard/rag/[col]
 * - 后端:
 *     GET    /api/rag/collections/:col/documents
 *     POST   /api/rag/collections/:col/documents  (multipart 或 json)
 *     DELETE /api/rag/collections/:col/documents/:doc
 *     GET    /api/rag/tasks/:task_id               (F1 异步任务状态)
 */
export default function RagCollectionDetailPage() {
  const params = useParams<{ col: string }>();
  const col = decodeURIComponent(params.col);
  const queryClient = useQueryClient();
  const fileRef = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState(false);
  const [uploadErr, setUploadErr] = useState<string | null>(null);
  const [textForm, setTextForm] = useState({ title: "", text: "" });
  const [showTextForm, setShowTextForm] = useState(false);
  const [activeTaskID, setActiveTaskID] = useState<string | null>(null);

  const { data: colMeta } = useQuery<Collection>({
    queryKey: ["collection", col],
    queryFn: () => api.get(`/api/meta/collections/${encodeURIComponent(col)}`),
  });

  const { data, isLoading, error } = useQuery({
    queryKey: ["rag-docs", col],
    queryFn: () => ragApi.listDocuments(col),
    refetchInterval: 3000,
  });

  const taskQ = useQuery({
    queryKey: ["rag-task", activeTaskID],
    queryFn: () => ragApi.getTask(activeTaskID!),
    enabled: !!activeTaskID,
    refetchInterval: (q) => {
      const st = q.state.data?.status;
      if (st === "success" || st === "failed") return false;
      return 1500;
    },
  });

  const onIngestQueued = (res: IngestResponse) => {
    if (res.task_id) setActiveTaskID(res.task_id);
    queryClient.invalidateQueries({ queryKey: ["rag-docs", col] });
  };

  const uploadMutation = useMutation({
    mutationFn: async (file: File) => {
      setUploading(true);
      setUploadErr(null);
      try {
        return await ragApi.uploadDocument(col, file, {
          title: file.name,
          source_type: file.name.endsWith(".md") ? "markdown" : "text",
        });
      } finally {
        setUploading(false);
      }
    },
    onSuccess: onIngestQueued,
    onError: (err: unknown) => {
      setUploadErr(err instanceof HttpError ? err.message : String(err));
    },
  });

  const ingestTextMutation = useMutation({
    mutationFn: () =>
      ragApi.ingestText(col, {
        title: textForm.title,
        text: textForm.text,
        source_type: "text",
      }),
    onSuccess: (res) => {
      onIngestQueued(res);
      setTextForm({ title: "", text: "" });
      setShowTextForm(false);
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (docID: string) => ragApi.deleteDocument(col, docID),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["rag-docs", col] }),
  });

  const items = data?.items ?? [];
  const task = taskQ.data;

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <div className="text-xs text-muted-foreground">
            <Link href="/dashboard/rag" className="hover:underline">
              知识库
            </Link>{" "}
            / {col}
          </div>
          <h1 className="mt-1 text-2xl font-semibold tracking-tight">{col}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {colMeta?.rag_config
              ? `embedding=${colMeta.rag_config.embedding_model || "(hash mock)"} · dim=${colMeta.rag_config.embedding_dim} · chunk=${colMeta.rag_config.chunk_size}/${colMeta.rag_config.chunk_overlap} · topK=${colMeta.rag_config.top_k_default}`
              : "加载配置中..."}
          </p>
        </div>
        <div className="flex gap-2">
          <Link
            href={`/dashboard/rag/${col}/wiki`}
            className="inline-flex h-9 items-center justify-center rounded-md border px-4 text-sm font-medium hover:bg-muted"
          >
            TitanWiki
          </Link>
          <Link
            href={`/dashboard/rag/${col}/chat`}
            className="inline-flex h-9 items-center justify-center rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground shadow hover:bg-primary/90"
          >
            进入问答
          </Link>
        </div>
      </div>

      {task && (
        <div
          className={`rounded-md border px-3 py-2 text-sm ${
            task.status === "failed"
              ? "border-destructive/40 bg-destructive/5 text-destructive"
              : task.status === "success"
                ? "border-emerald-500/30 bg-emerald-500/5 text-emerald-800"
                : "border-amber-500/40 bg-amber-50 text-amber-900"
          }`}
        >
          入库任务 <code className="font-mono text-xs">{task.task_id}</code>
          {" · "}
          {task.status}
          {typeof task.progress === "number" ? ` · ${task.progress}%` : ""}
          {task.error ? ` · ${task.error}` : ""}
          {task.doc_id ? ` · doc=${task.doc_id}` : ""}
        </div>
      )}

      <div className="rounded-lg border bg-card p-4 shadow-sm">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold">文档入库</h2>
          <button
            type="button"
            onClick={() => setShowTextForm((v) => !v)}
            className="text-xs text-primary hover:underline"
          >
            {showTextForm ? "切换为文件上传" : "切换为文本入库"}
          </button>
        </div>

        {!showTextForm ? (
          <div className="mt-3">
            <input
              ref={fileRef}
              type="file"
              accept=".txt,.md"
              disabled={uploading}
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (f) uploadMutation.mutate(f);
                if (fileRef.current) fileRef.current.value = "";
              }}
              className="block w-full text-sm file:mr-3 file:rounded-md file:border-0 file:bg-primary file:px-4 file:py-1.5 file:text-sm file:font-medium file:text-primary-foreground hover:file:bg-primary/90"
            />
            {uploading && (
              <p className="mt-2 text-xs text-muted-foreground">上传中...</p>
            )}
            {uploadErr && (
              <p className="mt-2 text-xs text-red-500">{uploadErr}</p>
            )}
          </div>
        ) : (
          <form
            className="mt-3 space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              if (textForm.title && textForm.text) {
                ingestTextMutation.mutate();
              }
            }}
          >
            <input
              required
              placeholder="标题"
              value={textForm.title}
              onChange={(e) => setTextForm({ ...textForm, title: e.target.value })}
              className="flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm"
            />
            <textarea
              required
              placeholder="正文内容"
              value={textForm.text}
              onChange={(e) => setTextForm({ ...textForm, text: e.target.value })}
              className="min-h-[120px] w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
            />
            <div className="flex justify-end">
              <button
                type="submit"
                disabled={ingestTextMutation.isPending}
                className="inline-flex h-9 items-center justify-center rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground shadow hover:bg-primary/90 disabled:opacity-50"
              >
                {ingestTextMutation.isPending ? "入库中..." : "入库"}
              </button>
            </div>
            {ingestTextMutation.error instanceof HttpError && (
              <p className="text-xs text-red-500">
                {ingestTextMutation.error.message}
              </p>
            )}
          </form>
        )}
      </div>

      <div>
        <h2 className="mb-2 text-sm font-semibold">文档列表 ({items.length})</h2>
        {isLoading && <p className="text-sm text-muted-foreground">加载中...</p>}
        {error instanceof HttpError && (
          <p className="text-sm text-red-500">{error.message}</p>
        )}
        <div className="overflow-hidden rounded-lg border">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-left text-xs uppercase text-muted-foreground">
              <tr>
                <th className="px-4 py-3">标题</th>
                <th className="px-4 py-3">类型</th>
                <th className="px-4 py-3">Chunks</th>
                <th className="px-4 py-3">Tokens</th>
                <th className="px-4 py-3">大小</th>
                <th className="px-4 py-3">创建时间</th>
                <th className="px-4 py-3 text-right">操作</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {items.length === 0 && (
                <tr>
                  <td colSpan={7} className="px-4 py-8 text-center text-muted-foreground">
                    暂无文档
                  </td>
                </tr>
              )}
              {items.map((d) => (
                <tr key={d.doc_id} className="hover:bg-muted/30">
                  <td className="px-4 py-3 font-medium">{d.title}</td>
                  <td className="px-4 py-3 text-muted-foreground">{d.source_type}</td>
                  <td className="px-4 py-3 text-muted-foreground">{d.chunk_count}</td>
                  <td className="px-4 py-3 text-muted-foreground">{d.token_count}</td>
                  <td className="px-4 py-3 text-muted-foreground">
                    {(d.size_bytes / 1024).toFixed(1)} KB
                  </td>
                  <td className="px-4 py-3 text-muted-foreground">
                    {new Date(d.created_at * 1000).toLocaleString()}
                  </td>
                  <td className="px-4 py-3 text-right">
                    <button
                      type="button"
                      onClick={() => {
                        if (confirm(`删除文档 ${d.title}？`)) {
                          deleteMutation.mutate(d.doc_id);
                        }
                      }}
                      className="text-sm text-red-500 hover:underline"
                    >
                      删除
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
