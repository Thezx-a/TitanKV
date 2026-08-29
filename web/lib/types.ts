// TitanKV 控制台涉及的 TypeScript 类型定义
// 字段名与后端 Go struct 的 json tag 保持一致 (snake_case)

/** 实时指标快照（对应 SSE /api/metrics/stream 推送与 /api/observability/metrics 首屏） */
export interface Metrics {
  /** 当前 QPS（Prometheus counter Δ/Δt；首帧常为 0） */
  qps: number;
  /** 延迟近似（有 sum/count 时为平均值 ms；非真 P50） */
  p50_ms: number;
  /** 真 P99；无 histogram quantile 时为 0 */
  p99_ms: number;
  /** 已用存储；未知时为 0 且 storage_known=false */
  storage_gb: number;
  storage_known?: boolean;
  /** 单进程演示拓扑下为 1 */
  node_count: number;
  /** Raft 教学路径外为 0 */
  leader_count: number;
  /** 采样时间戳（Unix 秒） */
  timestamp: number;
  /** prometheus_delta | none */
  qps_source?: string;
  /** true：延迟来自 avg，非分位数 */
  latency_approx?: boolean;
  /** minikv | memory | unknown — memory 时仪表盘黄条 */
  data_backend?: string;
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

/** TitanWiki 页 frontmatter */
export interface WikiFrontmatter {
  title: string;
  slug: string;
  type: string;
  tags?: string[];
  sources: string[];
  confidence?: string;
  contested?: boolean;
  updated_at?: number;
  compile_version?: number;
}

export interface WikiPage {
  frontmatter: WikiFrontmatter;
  body: string;
  summary: string;
}

export interface WikiEdge {
  from: string;
  to: string;
  rel: string;
}

export interface WikiIndexEntry {
  slug: string;
  title: string;
  type: string;
  summary: string;
}

export interface WikiIndexDoc {
  col: string;
  updated_at: number;
  entries: WikiIndexEntry[];
}

export interface WikiGraphView {
  slug: string;
  depth: number;
  out: WikiEdge[];
  in: WikiEdge[];
  neighbors?: Record<string, WikiEdge[]>;
}

export interface CompileTask {
  task_id: string;
  col: string;
  doc_id: string;
  status: "pending" | "running" | "success" | "failed";
  pages?: number;
  edges?: number;
  error?: string;
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
