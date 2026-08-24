package rag

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChatProvider 流式聊天模型. 每生成一个 token 调用 onToken; onToken 返回 error 可中断.
// 向量检索得到的上下文由调用方拼进 prompt, 这里只负责"吐 token".
type ChatProvider interface {
	StreamComplete(ctx context.Context, prompt string, onToken func(token string) error) error
	Provider() string
}

// ---- mockChatProvider: 无 API Key 时的本地 provider ----
//
// 从 prompt 中提取 <context>...</context>, 逐字吐回, 让 SSE 流式效果可演示.
type mockChatProvider struct{}

// NewMockChatProvider 构造本地 mock chat provider.
func NewMockChatProvider() ChatProvider { return &mockChatProvider{} }

func (m *mockChatProvider) Provider() string { return "local-mock" }

func (m *mockChatProvider) StreamComplete(_ context.Context, prompt string, onToken func(string) error) error {
	ctxText := extractTag(prompt, "context")
	resp := "根据知识库内容回答:\n"
	if ctxText != "" {
		resp += ctxText
	} else {
		resp += "（未检索到相关上下文，无法回答该问题）"
	}
	resp += "\n\n— 由 local-mock provider 生成（配置 RAG_CHAT_PROVIDER=openai 启用真实模型）"
	for _, r := range resp {
		if err := onToken(string(r)); err != nil {
			return err
		}
		time.Sleep(5 * time.Millisecond) // 让 SSE 帧肉眼可见
	}
	return nil
}

// extractTag 提取 prompt 中 <tag>...</tag> 之间的内容.
func extractTag(s, tag string) string {
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	j := strings.Index(s, closeTag)
	if j < 0 || j < i {
		return ""
	}
	return s[i+len(open) : j]
}

// ---- openaiChatProvider: OpenAI 兼容 /v1/chat/completions (stream=true) ----
type openaiChatProvider struct {
	apiBase string
	apiKey  string
	model   string
	http    *http.Client
}

// NewOpenAIChatProvider 构造走 OpenAI 兼容 API 的 chat provider.
func NewOpenAIChatProvider(apiBase, apiKey, model string) ChatProvider {
	return &openaiChatProvider{
		apiBase: strings.TrimRight(apiBase, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *openaiChatProvider) Provider() string { return "openai:" + p.model }

func (p *openaiChatProvider) StreamComplete(ctx context.Context, prompt string, onToken func(string) error) error {
	if p.apiKey == "" {
		return fmt.Errorf("openai chat: RAG_CHAT_API_KEY 未配置")
	}
	body, _ := json.Marshal(map[string]any{
		"model":   p.model,
		"stream":  true,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiBase+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("openai chat: http %d: %s", resp.StatusCode, string(b))
	}
	// 解析 SSE: 每行 "data: {json}", "data: [DONE]" 结束
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) == nil && len(chunk.Choices) > 0 {
			tok := chunk.Choices[0].Delta.Content
			if tok != "" {
				if err := onToken(tok); err != nil {
					return err
				}
			}
		}
	}
	return sc.Err()
}
