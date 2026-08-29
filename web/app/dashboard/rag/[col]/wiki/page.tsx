"use client";

import { useMemo, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ragApi } from "@/lib/rag";
import { HttpError } from "@/lib/api";
import type { WikiPage } from "@/lib/types";

/**
 * TitanWiki 浏览页：index + 页详情 + graph(depth≤2) + contested + 触发编译.
 * 路由: /dashboard/rag/[col]/wiki
 */
export default function TitanWikiPage() {
  const params = useParams<{ col: string }>();
  const col = decodeURIComponent(params.col);
  const queryClient = useQueryClient();
  const [selectedSlug, setSelectedSlug] = useState<string | null>(null);
  const [compileMsg, setCompileMsg] = useState<string | null>(null);
  const [compileErr, setCompileErr] = useState<string | null>(null);
  const [compileTaskID, setCompileTaskID] = useState<string | null>(null);

  const indexQ = useQuery({
    queryKey: ["wiki-index", col],
    queryFn: () => ragApi.wikiGetIndex(col),
    refetchInterval: 4000,
  });

  const contestedQ = useQuery({
    queryKey: ["wiki-contested", col],
    queryFn: () => ragApi.wikiListContested(col),
    refetchInterval: 5000,
  });

  const pageQ = useQuery({
    queryKey: ["wiki-page", col, selectedSlug],
    queryFn: () => ragApi.wikiGetPage(col, selectedSlug!),
    enabled: !!selectedSlug,
  });

  const graphQ = useQuery({
    queryKey: ["wiki-graph", col, selectedSlug],
    queryFn: () => ragApi.wikiGetGraph(col, selectedSlug!, 2),
    enabled: !!selectedSlug,
  });

  const compileTaskQ = useQuery({
    queryKey: ["wiki-compile-task", col, compileTaskID],
    queryFn: () => ragApi.wikiGetTask(col, compileTaskID!),
    enabled: !!compileTaskID,
    refetchInterval: (q) => {
      const st = q.state.data?.status;
      if (st === "success" || st === "failed") return false;
      return 1500;
    },
  });

  const compileMut = useMutation({
    mutationFn: () => ragApi.wikiCompile(col, {}),
    onSuccess: (res) => {
      setCompileErr(null);
      const id =
        res.task_id ||
        (res.task_ids && res.task_ids.length ? res.task_ids[0] : undefined);
      if (id) setCompileTaskID(id);
      setCompileMsg(
        `编译已入队: ${
          res.task_id ||
          (res.task_ids && res.task_ids.length
            ? `${res.task_ids.length} tasks`
            : "queued")
        }`,
      );
      queryClient.invalidateQueries({ queryKey: ["wiki-index", col] });
      queryClient.invalidateQueries({ queryKey: ["wiki-contested", col] });
    },
    onError: (err: unknown) => {
      setCompileMsg(null);
      setCompileErr(err instanceof HttpError ? err.message : String(err));
    },
  });

  const entries = indexQ.data?.entries ?? [];
  const contested = contestedQ.data?.items ?? [];
  const compileTask = compileTaskQ.data;

  const page: WikiPage | undefined = pageQ.data;

  const edgeLines = useMemo(() => {
    const g = graphQ.data;
    if (!g) return [];
    const lines: string[] = [];
    for (const e of g.out ?? []) lines.push(`${e.from} --${e.rel}→ ${e.to}`);
    for (const e of g.in ?? []) lines.push(`${e.from} --${e.rel}→ ${e.to} (in)`);
    if (g.neighbors) {
      for (const [node, edges] of Object.entries(g.neighbors)) {
        for (const e of edges) {
          lines.push(`[d2 ${node}] ${e.from} --${e.rel}→ ${e.to}`);
        }
      }
    }
    return lines;
  }, [graphQ.data]);

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <div className="text-xs text-muted-foreground">
            <Link href="/dashboard/rag" className="hover:underline">
              知识库
            </Link>{" "}
            /{" "}
            <Link
              href={`/dashboard/rag/${encodeURIComponent(col)}`}
              className="hover:underline"
            >
              {col}
            </Link>{" "}
            / Wiki
          </div>
          <h1 className="mt-1 text-2xl font-semibold tracking-tight">
            TitanWiki · {col}
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            编译型知识库：优先 wiki 页，向量检索兜底。冲突页标记 contested，不覆盖原文。
          </p>
        </div>
        <div className="flex shrink-0 gap-2">
          <Link
            href={`/dashboard/rag/${encodeURIComponent(col)}/chat`}
            className="inline-flex h-9 items-center rounded-md border px-4 text-sm hover:bg-muted"
          >
            向量问答
          </Link>
          <button
            type="button"
            disabled={compileMut.isPending}
            onClick={() => compileMut.mutate()}
            className="inline-flex h-9 items-center rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground shadow hover:bg-primary/90 disabled:opacity-50"
          >
            {compileMut.isPending ? "编译中…" : "编译整库"}
          </button>
        </div>
      </div>

      {(compileMsg || compileErr || compileTask) && (
        <div
          className={`rounded-md border px-3 py-2 text-sm ${
            compileErr || compileTask?.status === "failed"
              ? "border-destructive/40 bg-destructive/5 text-destructive"
              : compileTask?.status === "success"
                ? "border-emerald-500/30 bg-emerald-500/5 text-emerald-700"
                : "border-amber-500/40 bg-amber-50 text-amber-900"
          }`}
        >
          {compileErr ||
            (compileTask
              ? `编译任务 ${compileTask.task_id} · ${compileTask.status}` +
                (compileTask.pages != null ? ` · pages=${compileTask.pages}` : "") +
                (compileTask.edges != null ? ` · edges=${compileTask.edges}` : "") +
                (compileTask.error ? ` · ${compileTask.error}` : "")
              : compileMsg)}
        </div>
      )}

      {contested.length > 0 && (
        <div className="rounded-lg border border-amber-500/40 bg-amber-500/5 p-4">
          <h2 className="text-sm font-semibold text-amber-800">
            Contested（冲突） · {contested.length}
          </h2>
          <ul className="mt-2 space-y-1 text-sm">
            {contested.map((p) => (
              <li key={p.frontmatter.slug}>
                <button
                  type="button"
                  className="text-left text-amber-900 hover:underline"
                  onClick={() => setSelectedSlug(p.frontmatter.slug)}
                >
                  {p.frontmatter.title}{" "}
                  <span className="text-xs text-muted-foreground">
                    ({p.frontmatter.slug})
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}

      <div className="grid gap-6 lg:grid-cols-[280px_1fr]">
        <div className="rounded-lg border bg-card p-4 shadow-sm">
          <h2 className="text-sm font-semibold">
            Index · {entries.length} 页
          </h2>
          {indexQ.isLoading && (
            <p className="mt-2 text-xs text-muted-foreground">加载中…</p>
          )}
          {indexQ.error && (
            <p className="mt-2 text-xs text-destructive">
              {(indexQ.error as Error).message}
            </p>
          )}
          <ul className="mt-3 max-h-[28rem] space-y-1 overflow-y-auto text-sm">
            {entries.map((e) => (
              <li key={e.slug}>
                <button
                  type="button"
                  onClick={() => setSelectedSlug(e.slug)}
                  className={`w-full rounded px-2 py-1.5 text-left hover:bg-muted ${
                    selectedSlug === e.slug ? "bg-muted font-medium" : ""
                  }`}
                >
                  <div className="truncate">{e.title || e.slug}</div>
                  <div className="truncate text-xs text-muted-foreground">
                    {e.type} · {e.slug}
                  </div>
                </button>
              </li>
            ))}
            {!indexQ.isLoading && entries.length === 0 && (
              <li className="text-xs text-muted-foreground">
                尚无 wiki 页。入库文档后点「编译整库」。
              </li>
            )}
          </ul>
        </div>

        <div className="space-y-4">
          <div className="rounded-lg border bg-card p-4 shadow-sm">
            <h2 className="text-sm font-semibold">页面</h2>
            {!selectedSlug && (
              <p className="mt-2 text-sm text-muted-foreground">
                从左侧选择一个 slug。
              </p>
            )}
            {selectedSlug && pageQ.isLoading && (
              <p className="mt-2 text-sm text-muted-foreground">加载页…</p>
            )}
            {page && (
              <div className="mt-3 space-y-3">
                <div className="flex flex-wrap items-center gap-2">
                  <h3 className="text-lg font-medium">
                    {page.frontmatter.title}
                  </h3>
                  {page.frontmatter.contested && (
                    <span className="rounded bg-amber-100 px-2 py-0.5 text-xs text-amber-900">
                      contested
                    </span>
                  )}
                  <span className="text-xs text-muted-foreground">
                    {page.frontmatter.slug}
                  </span>
                </div>
                {page.summary && (
                  <p className="text-sm text-muted-foreground">{page.summary}</p>
                )}
                <pre className="max-h-80 overflow-auto whitespace-pre-wrap rounded border bg-muted/30 p-3 text-xs leading-relaxed">
                  {page.body || "(empty body)"}
                </pre>
                <p className="text-xs text-muted-foreground">
                  sources: {(page.frontmatter.sources || []).join(", ") || "—"}
                </p>
              </div>
            )}
          </div>

          <div className="rounded-lg border bg-card p-4 shadow-sm">
            <h2 className="text-sm font-semibold">Graph (depth≤2)</h2>
            {!selectedSlug && (
              <p className="mt-2 text-sm text-muted-foreground">选页后显示邻接边。</p>
            )}
            {selectedSlug && graphQ.isLoading && (
              <p className="mt-2 text-sm text-muted-foreground">加载图…</p>
            )}
            {edgeLines.length > 0 && (
              <ul className="mt-3 max-h-48 space-y-1 overflow-y-auto font-mono text-xs">
                {edgeLines.map((line, i) => (
                  <li key={`${i}-${line}`}>{line}</li>
                ))}
              </ul>
            )}
            {selectedSlug && !graphQ.isLoading && edgeLines.length === 0 && (
              <p className="mt-2 text-sm text-muted-foreground">无边。</p>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
