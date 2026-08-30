package rag

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChunkerVersion identifies the industrial chunker algorithm.
// Bump when Split semantics change so DocumentMeta can force re-chunk.
const ChunkerVersion = "v2"

// Chunker 把长文本切成带上下文重叠的块.
//
// V2: 分类 token 估算 + 软边界滑窗 + Markdown 层级路径 + 文档类型路由.
type Chunker struct {
	Size      int // 每块目标 token 数 (默认 512)
	Overlap   int // 重叠 token 数 (默认 64)
	Mode      string // markdown|pdf|code|faq|plain
	Estimator TokenEstimator // nil → package default
}

// NewChunker 构造, size<=0 时用默认 512, overlap<=0 用 64. Mode=markdown.
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
	return &Chunker{Size: size, Overlap: overlap, Mode: "markdown"}
}

func (c *Chunker) estimator() TokenEstimator {
	if c != nil && c.Estimator != nil {
		return c.Estimator
	}
	return getDefaultTokenEstimator()
}

func (c *Chunker) countTokens(s string) int {
	return c.estimator().Count(s)
}

// ChunkerFor picks a chunker strategy from filename / doc type.
func ChunkerFor(source, docType string) *Chunker {
	ch := NewChunker(512, 64)
	dt := strings.ToLower(strings.TrimSpace(docType))
	if dt == "" {
		dt = detectDocType(source)
	}
	switch dt {
	case "faq":
		ch.Mode = "faq"
	case "code":
		ch.Mode = "code"
	case "pdf":
		ch.Mode = "pdf"
	case "markdown", "md":
		ch.Mode = "markdown"
	default:
		// filename hints
		base := strings.ToLower(filepath.Base(source))
		if strings.Contains(base, "faq") {
			ch.Mode = "faq"
		} else {
			ch.Mode = "plain"
		}
	}
	return ch
}

func detectDocTypeFromText(source, text string) string {
	if dt := detectDocType(source); dt != "plain" {
		return dt
	}
	// inline / unknown: sniff FAQ
	trim := strings.TrimSpace(text)
	if strings.HasPrefix(trim, "Q:") || strings.HasPrefix(trim, "Q：") ||
		strings.HasPrefix(trim, "问:") || strings.HasPrefix(trim, "问：") {
		return "faq"
	}
	return "plain"
}

func detectDocType(source string) string {
	ext := strings.ToLower(filepath.Ext(source))
	switch ext {
	case ".md", ".markdown":
		return "markdown"
	case ".pdf":
		return "pdf"
	case ".go", ".cpp", ".h", ".hpp", ".c", ".py", ".js", ".ts", ".java", ".rs":
		return "code"
	default:
		if strings.Contains(strings.ToLower(filepath.Base(source)), "faq") {
			return "faq"
		}
		return "plain"
	}
}

// Chunk 单个切块结果 (内存中间结构, 入库时再拼 ChunkRecord).
type Chunk struct {
	Seq     int
	Heading string
	Text    string
}

// Split 按 Mode 路由切分.
func (c *Chunker) Split(text string) []Chunk {
	text = cleanText(text)
	if text == "" {
		return nil
	}
	switch c.Mode {
	case "faq":
		return c.splitFAQ(text)
	case "code":
		return c.splitCode(text)
	case "pdf", "plain":
		return c.splitPlain(text)
	default: // markdown
		return c.splitMarkdown(text)
	}
}

func (c *Chunker) splitMarkdown(text string) []Chunk {
	sections := splitByHeadingV2(text)
	var out []Chunk
	seq := 0
	for _, sec := range sections {
		if c.countTokens(sec.text) <= c.Size {
			out = append(out, Chunk{Seq: seq, Heading: sec.heading, Text: sec.text})
			seq++
			continue
		}
		for _, piece := range slideWindowWithBoundariesEst(sec.text, c.Size, c.Overlap, c.estimator()) {
			out = append(out, Chunk{Seq: seq, Heading: sec.heading, Text: piece})
			seq++
		}
	}
	return out
}

func (c *Chunker) splitPlain(text string) []Chunk {
	var out []Chunk
	seq := 0
	for _, piece := range slideWindowWithBoundariesEst(text, c.Size, c.Overlap, c.estimator()) {
		out = append(out, Chunk{Seq: seq, Text: piece})
		seq++
	}
	return out
}

func (c *Chunker) splitFAQ(text string) []Chunk {
	// Split on Q: / Q： at line start; keep Q+A as one chunk.
	lines := strings.Split(text, "\n")
	var blocks []string
	var cur strings.Builder
	flush := func() {
		s := strings.TrimSpace(cur.String())
		if s != "" {
			blocks = append(blocks, s)
		}
		cur.Reset()
	}
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if isFAQQuestion(trim) && cur.Len() > 0 {
			flush()
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	flush()
	if len(blocks) == 0 {
		return c.splitPlain(text)
	}
	out := make([]Chunk, 0, len(blocks))
	for i, b := range blocks {
		if c.countTokens(b) <= c.Size {
			out = append(out, Chunk{Seq: i, Heading: faqHeading(b), Text: b})
			continue
		}
		// oversized Q/A: soft-split but keep seq contiguous
		base := len(out)
		for _, piece := range slideWindowWithBoundariesEst(b, c.Size, c.Overlap, c.estimator()) {
			out = append(out, Chunk{Seq: base, Heading: faqHeading(b), Text: piece})
			base++
		}
	}
	// renumber seq
	for i := range out {
		out[i].Seq = i
	}
	return out
}

func isFAQQuestion(line string) bool {
	return strings.HasPrefix(line, "Q:") || strings.HasPrefix(line, "Q：") ||
		strings.HasPrefix(line, "问:") || strings.HasPrefix(line, "问：")
}

func faqHeading(block string) string {
	for _, line := range strings.Split(block, "\n") {
		trim := strings.TrimSpace(line)
		if isFAQQuestion(trim) {
			trim = strings.TrimPrefix(trim, "Q:")
			trim = strings.TrimPrefix(trim, "Q：")
			trim = strings.TrimPrefix(trim, "问:")
			trim = strings.TrimPrefix(trim, "问：")
			return strings.TrimSpace(trim)
		}
	}
	return ""
}

var codeBoundaryRe = regexp.MustCompile(`(?m)^(func |def |class |void |int |bool |public |private |protected |fn |impl |struct |type )`)

func (c *Chunker) splitCode(text string) []Chunk {
	// Prefer function/class boundaries; fall back to blank-line groups then soft window.
	idxs := codeBoundaryRe.FindAllStringIndex(text, -1)
	if len(idxs) == 0 {
		return c.splitPlain(text)
	}
	var parts []string
	for i, loc := range idxs {
		start := loc[0]
		end := len(text)
		if i+1 < len(idxs) {
			end = idxs[i+1][0]
		}
		parts = append(parts, strings.TrimSpace(text[start:end]))
	}
	// preamble before first func
	if idxs[0][0] > 0 {
		pre := strings.TrimSpace(text[:idxs[0][0]])
		if pre != "" {
			parts = append([]string{pre}, parts...)
		}
	}
	var out []Chunk
	seq := 0
	for _, p := range parts {
		if p == "" {
			continue
		}
		if c.countTokens(p) <= c.Size {
			out = append(out, Chunk{Seq: seq, Heading: codeHeading(p), Text: p})
			seq++
			continue
		}
		for _, piece := range slideWindowWithBoundariesEst(p, c.Size, c.Overlap, c.estimator()) {
			out = append(out, Chunk{Seq: seq, Heading: codeHeading(p), Text: piece})
			seq++
		}
	}
	return out
}

func codeHeading(block string) string {
	for _, line := range strings.Split(block, "\n") {
		trim := strings.TrimSpace(line)
		if codeBoundaryRe.MatchString(trim + "\n") || codeBoundaryRe.MatchString(trim) {
			if len(trim) > 80 {
				return trim[:80]
			}
			return trim
		}
	}
	return ""
}

type section struct {
	heading string
	text    string
}

var mdHeadingRe = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)

// splitByHeadingV2 recognizes #{1,6} and builds heading paths (parent → child).
func splitByHeadingV2(text string) []section {
	lines := strings.Split(text, "\n")
	var out []section
	var cur section
	stack := make([]string, 0, 6) // path by depth index 0=# ...

	flush := func() {
		cur.text = strings.TrimRight(cur.text, "\n")
		if cur.text != "" {
			out = append(out, cur)
		}
		cur = section{}
	}

	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if m := mdHeadingRe.FindStringSubmatch(trimmed); m != nil {
			flush()
			level := len(m[1]) // 1..6
			title := strings.TrimSpace(m[2])
			// shrink stack to parent levels
			if level-1 < len(stack) {
				stack = stack[:level-1]
			}
			for len(stack) < level-1 {
				stack = append(stack, "")
			}
			stack = append(stack[:level-1], title)
			cur.heading = joinHeadingPath(stack)
			continue
		}
		if cur.text != "" || strings.TrimSpace(line) != "" {
			cur.text += line + "\n"
		}
	}
	flush()
	return out
}

func joinHeadingPath(stack []string) string {
	parts := make([]string, 0, len(stack))
	for _, s := range stack {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " → ")
}

// slideWindowWithBoundaries prefers sentence/paragraph ends; hard-cuts at size*0.9 if none.
func slideWindowWithBoundaries(text string, size, overlap int) []string {
	return slideWindowWithBoundariesEst(text, size, overlap, getDefaultTokenEstimator())
}

func slideWindowWithBoundariesEst(text string, size, overlap int, est TokenEstimator) []string {
	if est == nil {
		est = getDefaultTokenEstimator()
	}
	runesh := []rune(text)
	if len(runesh) == 0 {
		return nil
	}
	if est.Count(text) <= size {
		return []string{strings.TrimSpace(text)}
	}
	var out []string
	start := 0
	for start < len(runesh) {
		targetEnd := advanceByEstimator(runesh, start, size, est)
		if targetEnd >= len(runesh) {
			piece := strings.TrimSpace(string(runesh[start:]))
			if piece != "" {
				out = append(out, piece)
			}
			break
		}
		minEnd := advanceByEstimator(runesh, start, int(float64(size)*0.55), est)
		if minEnd < start+1 {
			minEnd = start + 1
		}
		cut := findSoftBoundary(runesh, minEnd, targetEnd)
		if cut < 0 {
			cut = advanceByEstimator(runesh, start, int(float64(size)*0.9), est)
			if cut <= start {
				cut = targetEnd
			}
		}
		piece := strings.TrimSpace(string(runesh[start:cut]))
		if piece != "" {
			out = append(out, piece)
		}
		next := retreatByEstimator(runesh, cut, overlap, est)
		if next <= start {
			next = cut
		}
		if snap := findSoftBoundary(runesh, next, cut); snap > next {
			next = snap
		}
		if next <= start {
			next = cut
		}
		start = next
	}
	return out
}


// advanceByEstimator grows from start until est.Count reaches tokens (rune-index end).
func advanceByEstimator(runes []rune, start, tokens int, est TokenEstimator) int {
	if tokens <= 0 || start >= len(runes) {
		return start
	}
	// Always advance at least one rune (even if a single rune exceeds budget).
	best := start + 1
	lo, hi := start+1, len(runes)
	for lo <= hi {
		mid := (lo + hi) / 2
		if mid > len(runes) {
			break
		}
		n := est.Count(string(runes[start:mid]))
		if n <= tokens {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best
}

func retreatByEstimator(runes []rune, end, tokens int, est TokenEstimator) int {
	if tokens <= 0 || end <= 0 {
		return end
	}
	lo, hi := 0, end-1
	best := 0
	for lo <= hi {
		mid := (lo + hi) / 2
		n := est.Count(string(runes[mid:end]))
		if n <= tokens {
			best = mid
			hi = mid - 1
		} else {
			lo = mid + 1
		}
	}
	return best
}

func advanceByTokens(runes []rune, start, tokens int) int {
	if tokens <= 0 || start >= len(runes) {
		return start
	}
	acc := 0.0
	i := start
	for i < len(runes) && acc < float64(tokens) {
		acc += tokenWeight(runes[i])
		i++
	}
	return i
}

func retreatByTokens(runes []rune, end, tokens int) int {
	if tokens <= 0 || end <= 0 {
		return end
	}
	acc := 0.0
	i := end
	for i > 0 && acc < float64(tokens) {
		i--
		acc += tokenWeight(runes[i])
	}
	return i
}

func findSoftBoundary(runes []rune, from, to int) int {
	if from < 0 {
		from = 0
	}
	if to > len(runes) {
		to = len(runes)
	}
	if from >= to {
		return -1
	}
	// Prefer Chinese/English sentence end, then newline, scanning backward.
	for i := to - 1; i >= from; i-- {
		r := runes[i]
		if r == '。' || r == '！' || r == '？' || r == '.' || r == '!' || r == '?' {
			return i + 1
		}
	}
	for i := to - 1; i >= from; i-- {
		if runes[i] == '\n' {
			return i + 1
		}
	}
	return -1
}

// estimateTokens classifies UTF-8: ASCII alnum ~0.25/rune (4≈1), CJK ~0.67/rune (~1.5字≈1),
// whitespace/punct ~0.5.
func estimateTokensHeuristic(s string) int {
	if s == "" {
		return 0
	}
	var acc float64
	for _, r := range s {
		acc += tokenWeight(r)
	}
	n := int(acc + 0.5)
	if n < 1 {
		return 1
	}
	return n
}

func tokenWeight(r rune) float64 {
	switch {
	case unicode.Is(unicode.Han, r):
		return 1.0 / 1.5
	case r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
		return 0.25
	case unicode.IsSpace(r):
		return 0.5
	case unicode.IsPunct(r) || unicode.IsSymbol(r):
		return 0.5
	default:
		// other scripts: treat closer to CJK density
		if utf8.RuneLen(r) > 1 {
			return 1.0 / 1.5
		}
		return 0.25
	}
}

// estTokens keeps the old name as an alias for callers/tests.

// estimateTokens uses the package default TokenEstimator.
func estimateTokens(s string) int {
	return getDefaultTokenEstimator().Count(s)
}

func estTokens(s string) int { return estimateTokens(s) }

// slideWindow kept for compatibility; delegates to soft-boundary version.
func slideWindow(text string, size, overlap int) []string {
	return slideWindowWithBoundaries(text, size, overlap)
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

// legacy splitByHeading retained for any external refs; prefer V2.
func splitByHeading(text string) []section {
	return splitByHeadingV2(text)
}
