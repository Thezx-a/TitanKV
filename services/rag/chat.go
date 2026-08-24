package rag

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ChatOrchestrator 流式问答编排 (RagKv.md §8.3):
//
//	retrieve → 组装 prompt → chat.StreamComplete → SSE token
//	           → 写会话历史 (rag:chat: 前缀, 可回放)
type ChatOrchestrator struct {
	retriever *Retriever
	chat      ChatProvider
	store     *Store
	topK      int
}

// NewChatOrchestrator 构造问答编排器.
func NewChatOrchestrator(r *Retriever, cp ChatProvider, s *Store, topK int) *ChatOrchestrator {
	if topK <= 0 {
		topK = 5
	}
	return &ChatOrchestrator{retriever: r, chat: cp, store: s, topK: topK}
}

// Ask 流式问答. onToken 每个 token 回调一次; 返回完整 answer 与引用 doc_id 列表.
// uid/sid 用于会话历史; 为空时跳过历史写入.
func (o *ChatOrchestrator) Ask(
	ctx context.Context, col, query, uid, sid string, topK int,
	onToken func(string) error,
) (answer string, citations []string, err error) {
	if topK <= 0 {
		topK = o.topK
	}
	start := time.Now()

	// 1) 检索
	hits, err := o.retriever.Retrieve(ctx, col, query, topK)
	if err != nil {
		return "", nil, fmt.Errorf("retrieve: %w", err)
	}

	// 2) 组装 prompt
	prompt := assemblePrompt(query, hits)

	// 3) 流式生成, 同时累积 answer
	var b strings.Builder
	err = o.chat.StreamComplete(ctx, prompt, func(tok string) error {
		b.WriteString(tok)
		return onToken(tok)
	})
	if err != nil {
		return b.String(), nil, err
	}
	answer = b.String()

	// 4) 引用 doc_id 去重
	seen := make(map[string]bool)
	for _, h := range hits {
		if !seen[h.DocID] {
			seen[h.DocID] = true
			citations = append(citations, h.DocID)
		}
	}

	// 5) 写会话历史 (uid/sid 为空跳过)
	if uid != "" && sid != "" {
		// seq 用时间戳保证递增 (MVP; 严谨应查最大 seq)
		base := time.Now().Unix()
		_ = o.store.AppendChat(uid, sid, "user", query, nil, int(base*2))
		_ = o.store.AppendChat(uid, sid, "assistant", answer, citations, int(base*2+1))
	}

	// 6) 异步写 query log (这里同步简化)
	_ = o.store.Put(qlogKey(col, fmt.Sprintf("%d", start.UnixNano())), queryLogJSON(col, query, citations, time.Since(start).Milliseconds()))

	return answer, citations, nil
}

// assemblePrompt 组装三段式 prompt: system + <context> + 用户问题.
func assemblePrompt(query string, hits []RetrievalHit) string {
	var b strings.Builder
	b.WriteString("你是一个知识库助手，根据下面上下文回答问题，不要编造，无法回答时说明。\n\n<context>\n")
	for i, h := range hits {
		heading := h.Heading
		if heading == "" {
			heading = "-"
		}
		fmt.Fprintf(&b, "[%d] %s (doc:%s, heading:%s, score:%.2f)\n", i+1, h.Text, h.DocID, heading, h.Score)
	}
	b.WriteString("</context>\n\n用户问题: ")
	b.WriteString(query)
	return b.String()
}

// queryLogJSON 手拼避免循环依赖 (QueryLog 已有 json tag, 这里简单序列化).
func queryLogJSON(col, query string, citations []string, latencyMS int64) string {
	return fmt.Sprintf(`{"col":%q,"query":%q,"hits":%q,"latency_ms":%d,"created_at":%d}`,
		col, query, fmt.Sprint(citations), latencyMS, time.Now().Unix())
}
