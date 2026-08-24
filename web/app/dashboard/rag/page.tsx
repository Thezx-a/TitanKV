"use client";

import { useState } from "react";
import Link from "next/link";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, HttpError } from "@/lib/api";
import type {
  Collection,
  CollectionInput,
  RagCollectionConfig,
} from "@/lib/types";

/**
 * 知识库管理页 (RAG Collection).
 *
 * 与 /dashboard/collections 的区别：
 *   - 仅展示 / 创建 type === "rag" 的 Collection
 *   - 创建时必须填写 RagCollectionConfig
 *
 * 后端：services/meta/handler.go POST /api/meta/collections { type:"rag", rag_config:{...} }
 */
export default function RagCollectionsPage() {
  const queryClient = useQueryClient();
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState<CollectionInput>({
    name: "",
    type: "rag",
    ttl_seconds: 0,
    schema: {},
    rag_config: defaultRagConfig(),
  });

  const { data, isLoading, error } = useQuery<{ items: Collection[]; count: number }>({
    queryKey: ["collections"],
    queryFn: () => api.get("/api/meta/collections"),
  });

  const createMutation = useMutation({
    mutationFn: (input: CollectionInput) =>
      api.post<Collection>("/api/meta/collections", input),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["collections"] });
      setShowForm(false);
      setForm({
        name: "",
        type: "rag",
        ttl_seconds: 0,
        schema: {},
        rag_config: defaultRagConfig(),
      });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (name: string) => api.delete(`/api/meta/collections/${name}`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["collections"] }),
  });

  const items = (data?.items ?? []).filter((c) => (c.type ?? "kv") === "rag");

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">知识库</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            RAG 知识库 = 向量旁路索引 + minikv 文本存储。点击进入可上传文档与流式问答。
          </p>
        </div>
        <button
          type="button"
          onClick={() => setShowForm((v) => !v)}
          className="inline-flex h-9 items-center justify-center rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground shadow hover:bg-primary/90"
        >
          {showForm ? "取消" : "新建知识库"}
        </button>
      </div>

      {showForm && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            createMutation.mutate(form);
          }}
          className="space-y-4 rounded-lg border bg-card p-6 shadow-sm"
        >
          <div className="grid grid-cols-2 gap-4">
            <Field label="知识库名称">
              <input
                required
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                className={inputCls}
                placeholder="my-kb"
              />
            </Field>
            <Field label="Embedding 维度">
              <input
                type="number"
                min={1}
                value={form.rag_config?.embedding_dim ?? 512}
                onChange={(e) =>
                  setForm({
                    ...form,
                    rag_config: {
                      ...(form.rag_config as RagCollectionConfig),
                      embedding_dim: Number(e.target.value),
                    },
                  })
                }
                className={inputCls}
              />
            </Field>
            <Field label="Embedding 模型">
              <input
                value={form.rag_config?.embedding_model ?? ""}
                onChange={(e) =>
                  setForm({
                    ...form,
                    rag_config: {
                      ...(form.rag_config as RagCollectionConfig),
                      embedding_model: e.target.value,
                    },
                  })
                }
                className={inputCls}
                placeholder="bge-small-zh-v1.5 (留空走 hash mock)"
              />
            </Field>
            <Field label="距离度量">
              <select
                value={form.rag_config?.distance_metric ?? "cosine"}
                onChange={(e) =>
                  setForm({
                    ...form,
                    rag_config: {
                      ...(form.rag_config as RagCollectionConfig),
                      distance_metric: e.target
                        .value as RagCollectionConfig["distance_metric"],
                    },
                  })
                }
                className={inputCls}
              >
                <option value="cosine">cosine</option>
                <option value="dot">dot</option>
                <option value="l2">l2</option>
              </select>
            </Field>
            <Field label="Chunk Size (tokens)">
              <input
                type="number"
                min={1}
                value={form.rag_config?.chunk_size ?? 512}
                onChange={(e) =>
                  setForm({
                    ...form,
                    rag_config: {
                      ...(form.rag_config as RagCollectionConfig),
                      chunk_size: Number(e.target.value),
                    },
                  })
                }
                className={inputCls}
              />
            </Field>
            <Field label="Chunk Overlap">
              <input
                type="number"
                min={0}
                value={form.rag_config?.chunk_overlap ?? 64}
                onChange={(e) =>
                  setForm({
                    ...form,
                    rag_config: {
                      ...(form.rag_config as RagCollectionConfig),
                      chunk_overlap: Number(e.target.value),
                    },
                  })
                }
                className={inputCls}
              />
            </Field>
            <Field label="TopK 默认值">
              <input
                type="number"
                min={1}
                value={form.rag_config?.top_k_default ?? 5}
                onChange={(e) =>
                  setForm({
                    ...form,
                    rag_config: {
                      ...(form.rag_config as RagCollectionConfig),
                      top_k_default: Number(e.target.value),
                    },
                  })
                }
                className={inputCls}
              />
            </Field>
            <Field label="可见性">
              <select
                value={form.rag_config?.visibility ?? "private"}
                onChange={(e) =>
                  setForm({
                    ...form,
                    rag_config: {
                      ...(form.rag_config as RagCollectionConfig),
                      visibility: e.target
                        .value as RagCollectionConfig["visibility"],
                    },
                  })
                }
                className={inputCls}
              >
                <option value="private">private</option>
                <option value="shared">shared</option>
              </select>
            </Field>
          </div>
          {createMutation.error instanceof HttpError && (
            <p className="text-sm text-red-500">
              {createMutation.error.message}
            </p>
          )}
          <div className="flex justify-end">
            <button
              type="submit"
              disabled={createMutation.isPending}
              className="inline-flex h-9 items-center justify-center rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground shadow hover:bg-primary/90 disabled:opacity-50"
            >
              {createMutation.isPending ? "提交中..." : "创建"}
            </button>
          </div>
        </form>
      )}

      {isLoading && <p className="text-sm text-muted-foreground">加载中...</p>}
      {error instanceof HttpError && (
        <p className="text-sm text-red-500">{error.message}</p>
      )}

      <div className="overflow-hidden rounded-lg border">
        <table className="w-full text-sm">
          <thead className="bg-muted/50 text-left text-xs uppercase text-muted-foreground">
            <tr>
              <th className="px-4 py-3">名称</th>
              <th className="px-4 py-3">Embedding</th>
              <th className="px-4 py-3">维度</th>
              <th className="px-4 py-3">Chunk</th>
              <th className="px-4 py-3">TopK</th>
              <th className="px-4 py-3">创建时间</th>
              <th className="px-4 py-3 text-right">操作</th>
            </tr>
          </thead>
          <tbody className="divide-y">
            {items.length === 0 && (
              <tr>
                <td colSpan={7} className="px-4 py-8 text-center text-muted-foreground">
                  暂无知识库，点击右上角 “新建知识库” 开始。
                </td>
              </tr>
            )}
            {items.map((c) => (
              <tr key={c.name} className="hover:bg-muted/30">
                <td className="px-4 py-3 font-medium">
                  <Link href={`/dashboard/rag/${c.name}`} className="hover:underline">
                    {c.name}
                  </Link>
                </td>
                <td className="px-4 py-3 text-muted-foreground">
                  {c.rag_config?.embedding_model || "(hash mock)"}
                </td>
                <td className="px-4 py-3 text-muted-foreground">
                  {c.rag_config?.embedding_dim ?? "-"}
                </td>
                <td className="px-4 py-3 text-muted-foreground">
                  {c.rag_config?.chunk_size ?? "-"}/{c.rag_config?.chunk_overlap ?? 0}
                </td>
                <td className="px-4 py-3 text-muted-foreground">
                  {c.rag_config?.top_k_default ?? "-"}
                </td>
                <td className="px-4 py-3 text-muted-foreground">
                  {new Date(c.created_at * 1000).toLocaleString()}
                </td>
                <td className="px-4 py-3 text-right">
                  <button
                    type="button"
                    onClick={() => {
                      if (confirm(`删除知识库 ${c.name}？`)) {
                        deleteMutation.mutate(c.name);
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
  );
}

function defaultRagConfig(): RagCollectionConfig {
  return {
    embedding_model: "",
    embedding_dim: 512,
    distance_metric: "cosine",
    chunk_size: 512,
    chunk_overlap: 64,
    top_k_default: 5,
    rerank_enabled: false,
    allowed_file_types: ["txt", "md"],
    max_doc_size_mb: 10,
    owner: "",
    visibility: "private",
  };
}

const inputCls =
  "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm";

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <label className="text-sm font-medium">{label}</label>
      {children}
    </div>
  );
}
