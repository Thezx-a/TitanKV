// Package rag 是 TitanKV 的检索增强生成 (RAG) 编排服务.
//
// 设计依据: RagKv.md
//   - 不动 LSM / minikv_server / skynet. 复用 services/data 的 TCP 客户端直连 minikv.
//   - 向量旁路索引 (SideIndex): 向量不进 minikv, 只存本服务内存 + 快照文件.
//   - 原文 chunk / meta / task / chat 历史进 minikv, 按前缀化 key 组织,
//     利用 minikv Scan 做前缀遍历 (见 store.go 的 key 空间设计).
//
// 主链: Client → skynet → Gin → services/rag → minikv_server → LSM
package rag

import (
	"os"
	"strconv"
	"strings"
)

// Config 汇总 RAG 服务的全部配置项, 全部从环境变量读取.
// 契约与 RagKv.md §13 一致, 命名保留可被运维文档直接引用.
type Config struct {
	// 服务
	Addr         string // RAG_ADDR (兼容 RAG_SERVICE_PORT), 默认 :8085
	MinikvAddr   string // MINIKV_ADDR, 默认 127.0.0.1:8888
	IndexDir     string // RAG_INDEX_DIR, 默认 ./rag_index
	MaxDocSizeMB int    // RAG_MAX_DOC_SIZE_MB, 默认 20
	DefaultTopK  int    // RAG_DEFAULT_TOPK, 默认 5
	EnableRerank bool   // RAG_ENABLE_RERANK, 默认 false
	IndexType    string // RAG_INDEX_TYPE: brute|hnsw, 默认 hnsw

	// Embedding provider
	EmbeddingProvider string // RAG_EMBEDDING_PROVIDER: local|openai
	EmbeddingModel    string // RAG_EMBEDDING_MODEL
	EmbeddingDim      int    // RAG_EMBEDDING_DIM, 默认 384 (local), 1536 (openai 3-small)
	EmbeddingBaseURL  string // RAG_EMBEDDING_BASE_URL
	EmbeddingAPIKey   string // RAG_EMBEDDING_API_KEY
	EmbeddingBatch    int    // RAG_EMBEDDING_BATCH_SIZE, 默认 32

	// Chat provider
	ChatProvider string // RAG_CHAT_PROVIDER: local|openai
	ChatModel    string // RAG_CHAT_MODEL
	ChatBaseURL  string // RAG_CHAT_BASE_URL
	ChatAPIKey   string // RAG_CHAT_API_KEY

	// Industrial / W0
	AsyncIngest        bool   // RAG_ASYNC_INGEST, 默认 true
	IngestWorkers      int    // RAG_INGEST_WORKERS, 默认 4
	IngestQueueSize    int    // RAG_INGEST_QUEUE_SIZE, 默认 256
	PDFCommand         string // RAG_PDF_COMMAND, 默认 pdftotext
	EnableQueryRewrite bool   // RAG_ENABLE_QUERY_REWRITE, 默认 false
	HistoryTurns       int    // RAG_HISTORY_TURNS, 默认 3
	CacheTTLHours      int    // RAG_CACHE_TTL_HOURS, 默认 72

	// TitanWiki / W1
	EnableWiki     bool // RAG_ENABLE_WIKI, 默认 true
	WikiLLM        bool // RAG_WIKI_LLM, 默认 false（演示用规则 summary；开则走 ChatProvider）
	WikiWorkers    int  // RAG_WIKI_WORKERS, 默认 2
	WikiQueueSize  int  // RAG_WIKI_QUEUE_SIZE, 默认 64
	AutoCompile    bool // RAG_WIKI_AUTO_COMPILE, 默认 true（ingest success 后自动 enqueue）
}

// LoadConfig 从环境变量加载配置, 缺省值保证空环境也能启动 (MVP 可运行).
func LoadConfig() Config {
	return Config{
		Addr:               getenv("RAG_ADDR", getenv("RAG_SERVICE_PORT", ":8085")),
		MinikvAddr:         getenv("MINIKV_ADDR", "127.0.0.1:8888"),
		IndexDir:           getenv("RAG_INDEX_DIR", "./rag_index"),
		MaxDocSizeMB:       getenvInt("RAG_MAX_DOC_SIZE_MB", 20),
		DefaultTopK:        getenvInt("RAG_DEFAULT_TOPK", 5),
		EnableRerank:       getenvBool("RAG_ENABLE_RERANK", false),
		IndexType:          strings.ToLower(getenv("RAG_INDEX_TYPE", "hnsw")),
		EmbeddingProvider:  strings.ToLower(getenv("RAG_EMBEDDING_PROVIDER", "local")),
		EmbeddingModel:     getenv("RAG_EMBEDDING_MODEL", "text-embedding-3-small"),
		EmbeddingDim:       getenvInt("RAG_EMBEDDING_DIM", 384),
		EmbeddingBaseURL:   getenv("RAG_EMBEDDING_BASE_URL", "https://api.openai.com/v1"),
		EmbeddingAPIKey:    getenv("RAG_EMBEDDING_API_KEY", ""),
		EmbeddingBatch:     getenvInt("RAG_EMBEDDING_BATCH_SIZE", 32),
		ChatProvider:       strings.ToLower(getenv("RAG_CHAT_PROVIDER", "local")),
		ChatModel:          getenv("RAG_CHAT_MODEL", "gpt-4o-mini"),
		ChatBaseURL:        getenv("RAG_CHAT_BASE_URL", "https://api.openai.com/v1"),
		ChatAPIKey:         getenv("RAG_CHAT_API_KEY", ""),
		AsyncIngest:        getenvBool("RAG_ASYNC_INGEST", true),
		IngestWorkers:      getenvInt("RAG_INGEST_WORKERS", 4),
		IngestQueueSize:    getenvInt("RAG_INGEST_QUEUE_SIZE", 256),
		PDFCommand:         getenv("RAG_PDF_COMMAND", "pdftotext"),
		EnableQueryRewrite: getenvBool("RAG_ENABLE_QUERY_REWRITE", false),
		HistoryTurns:       getenvInt("RAG_HISTORY_TURNS", 3),
		CacheTTLHours:      getenvInt("RAG_CACHE_TTL_HOURS", 72),
		EnableWiki:         getenvBool("RAG_ENABLE_WIKI", true),
		WikiLLM:            getenvBool("RAG_WIKI_LLM", false),
		WikiWorkers:        getenvInt("RAG_WIKI_WORKERS", 2),
		WikiQueueSize:      getenvInt("RAG_WIKI_QUEUE_SIZE", 64),
		AutoCompile:        getenvBool("RAG_WIKI_AUTO_COMPILE", true),
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getenvInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
}
