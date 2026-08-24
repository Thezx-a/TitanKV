package rag

import (
	"strings"
	"unicode/utf8"
)

// Chunker 把长文本切成带上下文重叠的块.
//
// V1 固定滑动窗口 (RagKv.md §11): 按 token 估算切分 (4字节≈1token, 中文约2字≈1token),
// overlap 保证上下文连续性.
// V1.5 标题感知: 遇 Markdown 的 ## / ### 边界优先切, 使 chunk 的 heading 字段可标注.
type Chunker struct {
	Size    int // 每块目标 token 数 (默认 512)
	Overlap int // 重叠 token 数 (默认 64)
}

// NewChunker 构造, size<=0 时用默认 512, overlap<=0 用 64.
func NewChunker(size, overlap int) *Chunker {
	if size <= 0 {
		size = 512
	}
	if overlap <= 0 {
		overlap = 64
	}
	if overlap >= size {
		overlap = size / 4
	}
	return &Chunker{Size: size, Overlap: overlap}
}

// Chunk 单个切块结果 (内存中间结构, 入库时再拼 ChunkRecord).
type Chunk struct {
	Seq     int
	Heading string
	Text    string
}

// Split 切分文本. Markdown 优先按标题边界, 超长段再按窗口滑.
func (c *Chunker) Split(text string) []Chunk {
	text = cleanText(text)
	if text == "" {
		return nil
	}
	// 标题感知: 先按 Markdown 标题切段, 每段若超长再窗口切.
	sections := splitByHeading(text)
	var out []Chunk
	seq := 0
	for _, sec := range sections {
		// 一段换算成 token 估算 (1 token ≈ 2 中文/4 字节)
		if estTokens(sec.text) <= c.Size {
			out = append(out, Chunk{Seq: seq, Heading: sec.heading, Text: sec.text})
			seq++
			continue
		}
		// 超长, 滑动窗口切
		for _, piece := range slideWindow(sec.text, c.Size, c.Overlap) {
			out = append(out, Chunk{Seq: seq, Heading: sec.heading, Text: piece})
			seq++
		}
	}
	return out
}

type section struct {
	heading string
	text    string
}

// splitByHeading 按 ## / ### 切段, 第一段若无标题则 heading="".
func splitByHeading(text string) []section {
	lines := strings.Split(text, "\n")
	var out []section
	var cur section
	flush := func() {
		cur.text = strings.TrimRight(cur.text, "\n")
		if cur.text != "" {
			out = append(out, cur)
		}
		cur = section{}
	}
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
			flush()
			cur.heading = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, "## "), "## "))
		} else {
			if cur.text != "" || strings.TrimSpace(line) != "" {
				cur.text += line + "\n"
			}
		}
	}
	flush()
	return out
}

// slideWindow 按 token 估算做固定窗口 + overlap 切分.
func slideWindow(text string, size, overlap int) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	step := size - overlap
	if step <= 0 {
		step = size
	}
	// token 估算: 2 rune ≈ 1 token (中文), 取 rune 数除 2 近似
	var out []string
	for i := 0; i < len(runes); i += step {
		j := i + size*2 // rune 窗口
		if j > len(runes) {
			j = len(runes)
		}
		piece := strings.TrimSpace(string(runes[i:j]))
		if piece != "" {
			out = append(out, piece)
		}
		if j == len(runes) {
			break
		}
	}
	return out
}

// estTokens 粗略估算 token 数: utf8 字节数 / 4.
func estTokens(s string) int {
	return utf8.RuneCountInString(s) / 2
}

// cleanText 去多余空行, 统一换行.
func cleanText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(text)
}
