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
	retriever    *Retriever
	chat         ChatProvider
	store        *Store
	topK         int
	historyTurns int
}

// NewChatOrchestrator 构造问答编排器.
func NewChatOrchestrator(r *Retriever, cp ChatProvider, s *Store, topK int) *ChatOrchestrator {
	return NewChatOrchestratorWithHistory(r, cp, s, topK, 0)
}

// NewChatOrchestratorWithHistory sets how many prior turns to inject into the prompt.
func NewChatOrchestratorWithHistory(r *Retriever, cp ChatProvider, s *Store, topK, historyTurns int) *ChatOrchestrator {
	if topK <= 0 {
		topK = 5
	}
	if historyTurns < 0 {
		historyTurns = 0
	}
	return &ChatOrchestrator{retriever: r, chat: cp, store: s, topK: topK, historyTurns: historyTurns}
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
	defer func() { RagChatDuration.Observe(time.Since(start).Seconds()) }()

	// 1) 检索
	hits, err := o.retriever.Retrieve(ctx, col, query, topK)
	if err != nil {
		return "", nil, fmt.Errorf("retrieve: %w", err)
	}

	// 2) 可选历史
	var history []ChatMessage
	if o.historyTurns > 0 && uid != "" && sid != "" {
		if msgs, e := o.store.ListChat(uid, sid); e == nil {
			history = trimChatHistory(msgs, o.historyTurns)
		}
	}

	// 3) 组装 prompt
	prompt := assemblePromptWithHistory(query, hits, history)

	// 4) 流式生成, 同时累积 answer
	var b strings.Builder
	err = o.chat.StreamComplete(ctx, prompt, func(tok string) error {
		b.WriteString(tok)
		return onToken(tok)
	})
	if err != nil {
		return b.String(), nil, err
	}
	answer = b.String()

	// 5) 引用 doc_id 去重
	seen := make(map[string]bool)
	for _, h := range hits {
		if !seen[h.DocID] {
			seen[h.DocID] = true
			citations = append(citations, h.DocID)
		}
	}

	// 6) 写会话历史 (uid/sid 为空跳过)
	if uid != "" && sid != "" {
		base := time.Now().Unix()
		_ = o.store.AppendChat(uid, sid, "user", query, nil, int(base*2))
		_ = o.store.AppendChat(uid, sid, "assistant", answer, citations, int(base*2+1))
	}

	// 7) query log
	_ = o.store.Put(qlogKey(col, fmt.Sprintf("%d", start.UnixNano())), queryLogJSON(col, query, citations, time.Since(start).Milliseconds()))

	return answer, citations, nil
}

// AskWiki is wiki-first Q&A; citations use "wiki:{slug}" or raw doc_id.
func (o *ChatOrchestrator) AskWiki(
	ctx context.Context, col, query, uid, sid string, topK int,
	wq *WikiQuerier,
	onToken func(string) error,
) (answer string, citations []string, err error) {
	if topK <= 0 {
		topK = o.topK
	}
	start := time.Now()
	defer func() { RagChatDuration.Observe(time.Since(start).Seconds()) }()

	wikiHits, fallback, err := WikiFirstRetrieve(ctx, col, query, topK, wq, o.retriever)
	if err != nil {
		return "", nil, fmt.Errorf("wiki retrieve: %w", err)
	}

	var history []ChatMessage
	if o.historyTurns > 0 && uid != "" && sid != "" {
		if msgs, e := o.store.ListChat(uid, sid); e == nil {
			history = trimChatHistory(msgs, o.historyTurns)
		}
	}
	prompt := assembleWikiPrompt(query, wikiHits, fallback, history)

	var b strings.Builder
	err = o.chat.StreamComplete(ctx, prompt, func(tok string) error {
		b.WriteString(tok)
		return onToken(tok)
	})
	if err != nil {
		return b.String(), nil, err
	}
	answer = b.String()

	seen := map[string]bool{}
	for _, h := range wikiHits {
		key := "wiki:" + h.Slug
		if !seen[key] {
			seen[key] = true
			citations = append(citations, key)
		}
	}
	for _, h := range fallback {
		if !seen[h.DocID] {
			seen[h.DocID] = true
			citations = append(citations, h.DocID)
		}
	}

	if uid != "" && sid != "" {
		base := time.Now().Unix()
		_ = o.store.AppendChat(uid, sid, "user", query, nil, int(base*2))
		_ = o.store.AppendChat(uid, sid, "assistant", answer, citations, int(base*2+1))
	}
	_ = o.store.Put(qlogKey(col, fmt.Sprintf("%d", start.UnixNano())), queryLogJSON(col, query, citations, time.Since(start).Milliseconds()))
	return answer, citations, nil
}

// trimChatHistory keeps the last `turns` user/assistant pairs (2 msgs per turn).
func trimChatHistory(msgs []ChatMessage, turns int) []ChatMessage {
	if turns <= 0 || len(msgs) == 0 {
		return nil
	}
	n := turns * 2
	if n >= len(msgs) {
		return msgs
	}
	return msgs[len(msgs)-n:]
}

// assemblePrompt 组装三段式 prompt: system + <context> + 用户问题.
func assemblePrompt(query string, hits []RetrievalHit) string {
	return assemblePromptWithHistory(query, hits, nil)
}

// assemblePromptWithHistory injects recent chat turns when present.
func assemblePromptWithHistory(query string, hits []RetrievalHit, history []ChatMessage) string {
	var b strings.Builder
	b.WriteString("你是一个知识库助手，根据下面上下文回答问题，不要编造，无法回答时说明。\n\n")
	if len(history) > 0 {
		b.WriteString("<history>\n")
		for _, m := range history {
			fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
		}
		b.WriteString("</history>\n\n")
	}
	b.WriteString("<context>\n")
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
