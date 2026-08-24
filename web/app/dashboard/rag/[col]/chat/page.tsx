"use client";

import { useState, useRef, useEffect } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { ragApi } from "@/lib/rag";
import type { ChatEvent, RetrievalHit } from "@/lib/types";

/**
 * 知识库流式问答页 (SSE).
 *
 * 路由: /dashboard/rag/[col]/chat
 * 后端: POST /api/rag/collections/:col/chat  → event: token | citation | debug | end | error
 */
interface Message {
  role: "user" | "assistant";
  text: string;
  citations?: RetrievalHit[];
  pending?: boolean;
}

export default function RagChatPage() {
  const params = useParams<{ col: string }>();
  const col = decodeURIComponent(params.col);
  const [messages, setMessages] = useState<Message[]>([]);
  const [input, setInput] = useState("");
  const [streaming, setStreaming] = useState(false);
  const [sessionId] = useState(() =>
    typeof window === "undefined"
      ? ""
      : (crypto.randomUUID?.() ?? `sid_${Date.now()}`),
  );
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages]);

  async function handleSend(e: React.FormEvent) {
    e.preventDefault();
    if (!input.trim() || streaming) return;
    const q = input.trim();
    setInput("");

    setMessages((prev) => [
      ...prev,
      { role: "user", text: q },
      { role: "assistant", text: "", pending: true },
    ]);
    setStreaming(true);

    try {
      let buf = "";
      let citations: RetrievalHit[] = [];
      let assistantIdx = -1;
      setMessages((prev) => {
        const next = [...prev];
        assistantIdx = next.length - 1;
        return next;
      });

      for await (const ev of ragApi.chatStream(col, {
        query: q,
        session_id: sessionId,
        top_k: 5,
        history_turns: 3,
      })) {
        const data = (ev as ChatEvent).data as
          | { text?: string }
          | RetrievalHit
          | { rewrite_query?: string; top_k: number; prompt_tokens?: number }
          | { total_tokens?: number; latency_ms: number }
          | { code: string; msg: string };

        switch ((ev as ChatEvent).event) {
          case "token":
            buf += (data as { text: string }).text;
            setMessages((prev) => {
              const next = [...prev];
              if (assistantIdx >= 0) {
                next[assistantIdx] = {
                  ...next[assistantIdx],
                  text: buf,
                  pending: false,
                  citations,
                };
              }
              return next;
            });
            break;
          case "citation":
            citations = [...citations, data as RetrievalHit];
            setMessages((prev) => {
              const next = [...prev];
              if (assistantIdx >= 0) {
                next[assistantIdx] = {
                  ...next[assistantIdx],
                  citations,
                  pending: false,
                };
              }
              return next;
            });
            break;
          case "error":
            setMessages((prev) => {
              const next = [...prev];
              if (assistantIdx >= 0) {
                next[assistantIdx] = {
                  ...next[assistantIdx],
                  text: `[错误] ${(data as { msg: string }).msg}`,
                  pending: false,
                };
              }
              return next;
            });
            break;
          case "end":
            setMessages((prev) => {
              const next = [...prev];
              if (assistantIdx >= 0) {
                next[assistantIdx] = {
                  ...next[assistantIdx],
                  pending: false,
                };
              }
              return next;
            });
            break;
          default:
            // debug 事件忽略（不展示）
            break;
        }
      }
    } catch (err) {
      setMessages((prev) => {
        const next = [...prev];
        const i = next.length - 1;
        if (i >= 0 && next[i].role === "assistant") {
          next[i] = {
            ...next[i],
            text: `[异常] ${err instanceof Error ? err.message : String(err)}`,
            pending: false,
          };
        }
        return next;
      });
    } finally {
      setStreaming(false);
    }
  }

  return (
    <div className="flex h-[calc(100vh-3.5rem)] flex-col">
      <header className="border-b px-6 py-3">
        <div className="text-xs text-muted-foreground">
          <Link href="/dashboard/rag" className="hover:underline">
            知识库
          </Link>{" "}
          / <Link href={`/dashboard/rag/${col}`} className="hover:underline">{col}</Link>{" "}
          / 问答
        </div>
        <h1 className="mt-0.5 text-lg font-semibold">{col} · 问答</h1>
      </header>

      <main className="flex-1 space-y-4 overflow-y-auto px-6 py-4">
        {messages.length === 0 && (
          <p className="mt-12 text-center text-sm text-muted-foreground">
            输入问题开始问答。回答基于知识库中已入库文档检索后生成。
          </p>
        )}
        {messages.map((m, i) => (
          <div
            key={i}
            className={
              "flex " + (m.role === "user" ? "justify-end" : "justify-start")
            }
          >
            <div
              className={
                "max-w-[80%] rounded-lg px-4 py-2 text-sm shadow-sm " +
                (m.role === "user"
                  ? "bg-primary text-primary-foreground"
                  : "bg-card border")
              }
            >
              <div className="whitespace-pre-wrap break-words">
                {m.text}
                {m.pending && (
                  <span className="ml-1 inline-block h-3 w-1 animate-pulse bg-foreground/40" />
                )}
              </div>
              {m.citations && m.citations.length > 0 && (
                <div className="mt-2 space-y-1 border-t pt-2 text-xs">
                  <div className="font-medium text-muted-foreground">来源 ({m.citations.length})</div>
                  {m.citations.map((c, j) => (
                    <div key={j} className="truncate text-muted-foreground">
                      [{j + 1}] score={c.score.toFixed(3)} · {c.heading || c.doc_id}
                      {" · "}
                      {c.text.slice(0, 80)}
                      {c.text.length > 80 ? "..." : ""}
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        ))}
        <div ref={bottomRef} />
      </main>

      <footer className="border-t px-6 py-3">
        <form onSubmit={handleSend} className="flex gap-2">
          <input
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder="输入问题..."
            disabled={streaming}
            className="flex h-10 flex-1 rounded-md border border-input bg-background px-3 text-sm disabled:opacity-50"
          />
          <button
            type="submit"
            disabled={streaming || !input.trim()}
            className="inline-flex h-10 items-center justify-center rounded-md bg-primary px-5 text-sm font-medium text-primary-foreground shadow hover:bg-primary/90 disabled:opacity-50"
          >
            {streaming ? "回答中..." : "发送"}
          </button>
        </form>
      </footer>
    </div>
  );
}
