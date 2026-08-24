// TitanKV 控制台涉及的 TypeScript 类型定义
// 字段名与后端 Go struct 的 json tag 保持一致 (snake_case)

/** 实时指标快照（对应 SSE /api/metrics/stream 推送与 /api/observability/metrics 首屏） */
export interface Metrics {
  /** 当前 QPS（每秒请求数） */
  qps: number;
  /** P50 延迟，单位毫秒 */
  p50_ms: number;
  /** P99 延迟，单位毫秒 */
  p99_ms: number;
  /** 已用存储空间，单位 GB */
  storage_gb: number;
  /** 集群节点数 */
  node_count: number;
  /** Raft Leader 数 */
  leader_count: number;
  /** 采样时间戳（Unix 秒） */
  timestamp: number;
}

/** 用户 */
export interface User {
  id: string;
  username: string;
  /** 角色名：admin / writer / reader */
  role: UserRole;
  created_at: string;
  updated_at: string;
}

export type UserRole = "admin" | "writer" | "reader";

/** Collection 类型：普通 KV 命名空间 / RAG 知识库 */
export type CollectionType = "kv" | "rag";

/** 知识库配置（对应 services/meta/store.go RagCollectionConfig） */
export interface RagCollectionConfig {
  embedding_model: string;
  embedding_dim: number;
  distance_metric: "cosine" | "dot" | "l2";
  chunk_size: number;
  chunk_overlap: number;
  top_k_default: number;
  rerank_enabled: boolean;
  allowed_file_types: string[];
  max_doc_size_mb: number;
  owner: string;
  visibility: "private" | "shared";
}

/** Collection（键值集合的元数据） */
export interface Collection {
  name: string;
  /** "kv"（默认） | "rag" */
  type: CollectionType;
  /** TTL，单位秒；0 表示永久 */
  ttl_seconds: number;
  /** Schema 定义 */
  schema: Record<string, string>;
  /** 仅 type === "rag" 时存在 */
  rag_config?: RagCollectionConfig;
  created_at: number;
  updated_at: number;
}

export interface CollectionInput {
  name: string;
  type?: CollectionType;
  ttl_seconds?: number;
  schema?: Record<string, string>;
  rag_config?: RagCollectionConfig;
}

/** RAG 文档元数据（对应 services/rag/store.go DocumentMeta） */
export interface DocumentMeta {
  doc_id: string;
  collection: string;
  title: string;
  source_type: "text" | "markdown" | "pdf";
  content_hash: string;
  chunk_count: number;
  token_count: number;
  size_bytes: number;
  created_at: number;
  updated_at: number;
  extra?: Record<string, string>;
}

/** 文档 chunk（对应 services/rag/store.go ChunkRecord） */
export interface ChunkRecord {
  chunk_id: string;
  doc_id: string;
  collection: string;
  seq: number;
  text: string;
  token_count: number;
  char_start: number;
  char_end: number;
  heading?: string;
  page_num?: number;
  extra?: Record<string, string>;
}

/** 文档详情（含 chunks） */
export interface DocumentDetail extends DocumentMeta {
  chunks: ChunkRecord[];
}

/** 文档列表响应 */
export interface DocumentListResponse {
  items: DocumentMeta[];
  total: number;
}

/** 入库任务 */
export interface IngestTask {
  task_id: string;
  doc_id: string;
  status: "pending" | "running" | "success" | "failed";
  progress: number;
  error?: string;
}

/** 入库提交响应 */
export interface IngestResponse {
  task_id: string;
  doc_id: string;
  estimated_chunks: number;
}

/** 检索命中 */
export interface RetrievalHit {
  chunk_id: string;
  doc_id: string;
  score: number;
  text: string;
  heading?: string;
  page_num?: number;
}

/** 检索响应 */
export interface RetrieveResponse {
  hits: RetrievalHit[];
  rewrite_query?: string;
  latency_ms: number;
}

/** 检索请求 */
export interface RetrieveRequest {
  query: string;
  top_k?: number;
  filters?: Record<string, string>;
  debug?: boolean;
}

/** 流式问答请求 */
export interface ChatRequest {
  query: string;
  session_id?: string;
  top_k?: number;
  history_turns?: number;
}

/** SSE 事件：流式问答 */
export type ChatEvent =
  | { event: "token"; data: { text: string } }
  | { event: "citation"; data: RetrievalHit }
  | { event: "debug"; data: { rewrite_query?: string; top_k: number; prompt_tokens?: number } }
  | { event: "end"; data: { total_tokens?: number; latency_ms: number } }
  | { event: "error"; data: { code: string; msg: string } };

/** 键值对 */
export interface KVPair {
  key: string;
  value: string;
}

/** Scan 请求参数 */
export interface ScanParams {
  start?: string;
  end?: string;
}

/** 登录请求 */
export interface LoginRequest {
  username: string;
  password: string;
}

/** 登录响应（与后端 services/auth/handler.go LoginResponse 对齐） */
export interface LoginResponse {
  access_token: string;
  refresh_token: string;
  expires_in: number;
  token_type: string;
  user_id: string;
  role: UserRole;
}

/** 通用 API 错误 */
export interface ApiError {
  code: string;
  message: string;
  status: number;
  details?: unknown;
}
