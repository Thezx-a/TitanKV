import { API_BASE, api, apiFetch } from "./api";
import type {
  ChatEvent,
  ChatRequest,
  DocumentDetail,
  DocumentListResponse,
  IngestResponse,
  IngestTask,
  RetrieveRequest,
  RetrieveResponse,
} from "./types";

/**
 * RAG 知识库 API 封装。
 * 与 services/rag/handler.go 的路由对齐，所有路径前缀 /api/rag/collections/:col/*.
 * Gateway 已对该前缀做 RBAC 鉴权 (rag:ingest / rag:query).
 */
export const ragApi = {
  /** 上传文件入库 (multipart) */
  uploadDocument: (
    col: string,
    file: File,
    opts: { title?: string; source_type?: string } = {},
  ): Promise<IngestResponse> => {
    const form = new FormData();
    form.append("file", file);
    if (opts.title) form.append("title", opts.title);
    if (opts.source_type) form.append("source_type", opts.source_type);
    return apiFetch<IngestResponse>(
      `/api/rag/collections/${encodeURIComponent(col)}/documents`,
      { method: "POST", body: form, auth: true },
    );
  },

  /** 文本入库 (JSON) */
  ingestText: (
    col: string,
    body: { title: string; text: string; source_type?: string },
  ): Promise<IngestResponse> =>
    api.post<IngestResponse>(
      `/api/rag/collections/${encodeURIComponent(col)}/documents`,
      body,
    ),

  /** 文档列表 */
  listDocuments: (col: string): Promise<DocumentListResponse> =>
    api.get<DocumentListResponse>(
      `/api/rag/collections/${encodeURIComponent(col)}/documents`,
    ),

  /** 文档详情 (含 chunks) */
  getDocument: (col: string, docID: string): Promise<DocumentDetail> =>
    api.get<DocumentDetail>(
      `/api/rag/collections/${encodeURIComponent(col)}/documents/${encodeURIComponent(docID)}`,
    ),

  /** 删除文档 */
  deleteDocument: (col: string, docID: string): Promise<{ ok: boolean }> =>
    api.delete<{ ok: boolean }>(
      `/api/rag/collections/${encodeURIComponent(col)}/documents/${encodeURIComponent(docID)}`,
    ),

  /** 查询入库任务状态 */
  getTask: (taskID: string): Promise<IngestTask> =>
    api.get<IngestTask>(`/api/rag/tasks/${encodeURIComponent(taskID)}`),

  /** 检索 */
  retrieve: (col: string, body: RetrieveRequest): Promise<RetrieveResponse> =>
    api.post<RetrieveResponse>(
      `/api/rag/collections/${encodeURIComponent(col)}/retrieve`,
      body,
    ),

  /**
   * 流式问答 (SSE).
   * 返回一个可取消的异步迭代器；上层用 for-await-of 消费事件。
   *
   * 实现说明：
   *   - 走原生 fetch + ReadableStream 解析 SSE (event: xxx / data: {...})
   *   - 不依赖 EventSource，以便支持 POST + 自定义 header
   */
  chatStream: async function* (
    col: string,
    body: ChatRequest,
  ): AsyncGenerator<ChatEvent, void, unknown> {
    const token = readCookie("titan_token");
    const res = await fetch(
      `${API_BASE}/api/rag/collections/${encodeURIComponent(col)}/chat`,
      {
        method: "POST",
        credentials: "include",
        headers: {
          "Content-Type": "application/json",
          Accept: "text/event-stream",
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: JSON.stringify(body),
      },
    );

    if (!res.ok || !res.body) {
      const text = await res.text().catch(() => res.statusText);
      yield {
        event: "error",
        data: { code: "HTTP_ERROR", msg: `${res.status}: ${text}` },
      };
      return;
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder("utf-8");
    let buf = "";
    let currentEvent = "message";

    while (true) {
      const { value, done } = await reader.read();
      if (done) return;
      buf += decoder.decode(value, { stream: true });

      let idx: number;
      while ((idx = buf.indexOf("\n\n")) >= 0) {
        const block = buf.slice(0, idx);
        buf = buf.slice(idx + 2);
        for (const line of block.split("\n")) {
          if (line.startsWith("event:")) {
            currentEvent = line.slice(6).trim();
          } else if (line.startsWith("data:")) {
            const data = line.slice(5).trim();
            try {
              yield { event: currentEvent, data: JSON.parse(data) } as ChatEvent;
            } catch {
              // 忽略无法解析的 data 行
            }
            currentEvent = "message";
          }
        }
      }
    }
  },
};

function readCookie(name: string): string | null {
  if (typeof document === "undefined") return null;
  const match = document.cookie.match(
    new RegExp(
      "(?:^|; )" + name.replace(/([.$?*|{}()[\]\\/+^])/g, "\\$1") + "=([^;]*)",
    ),
  );
  return match ? decodeURIComponent(match[1]) : null;
}
