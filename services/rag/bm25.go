package rag

// ---- M1: BM25 稀疏检索通道 ----
//
// 定位: 与 HNSW 稠密通道并行的第一级检索通道, 两路 TopK 经 RRF(k=60) 融合
// (复用 query_expand.go 的 FuseHitsByRRF, 融合只看排名不看分值, 因此
// BM25 分数量纲与余弦不同无影响).
//
// 架构与向量 SideIndex 同构:
//   - 内存倒排 = 运行时真源 (查询零 minikv 往返, 满足热路径延迟要求)
//   - minikv posting 持久化 = 重启恢复 (rag:bm25:{col}:t:{term})
//   - Add 幂等, LoadFromStore/RebuildFromChunks 可自愈 (写入失败不阻断入库)
//
// key 布局 (store.go key 空间的 BM25 段):
//   rag:bm25:{col}:t:{term}   → {"postings":{"col/doc/seq":{"tf":n,"len":m}}}
//   rag:bm25:{col}:meta       → {"chunk_count":N,"total_len":L}
//   rag:bm25:{col}:d:{docID}  → ["term1","term2",...]   (删文档时定位受影响 term)
//
// 分词: ASCII 词小写化 (len>=2), CJK 连续串切 bigram (单字串保留单字).
// 中文 bigram 让改述型查询 ("为什么宕机后数据不会丢" → "宕机") 能命中,
// 这是 dense 词袋 hash embedding 覆盖不到的场景 (见 docs/rag-eval-baseline.md).

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/titan-kv/titan/services/data"
)

const (
	bm25K1 = 1.2  // 词频饱和参数
	bm25B  = 0.75 // 长度归一化参数
)

// bm25Posting is one (chunk, term) entry: term frequency and chunk BM25 token length.
type bm25Posting struct {
	TF  int `json:"tf"`
	Len int `json:"len"`
}

// bm25TermDoc is the persisted posting list of one term.
type bm25TermDoc struct {
	Postings map[string]bm25Posting `json:"postings"`
}

// bm25MetaDoc is the persisted per-collection statistics.
type bm25MetaDoc struct {
	ChunkCount int `json:"chunk_count"`
	TotalLen   int `json:"total_len"`
}

// bm25Col is the in-memory inverted state of one collection.
type bm25Col struct {
	postings   map[string]map[string]bm25Posting // term → chunkID → posting
	docTerms   map[string]map[string]struct{}    // docID → term set (删除定位)
	chunkLen   map[string]int                    // chunkID → BM25 token 数
	chunkCount int
	totalLen   int
}

func newBM25Col() *bm25Col {
	return &bm25Col{
		postings: make(map[string]map[string]bm25Posting),
		docTerms: make(map[string]map[string]struct{}),
		chunkLen: make(map[string]int),
	}
}

// BM25Index is the in-memory sparse retrieval lane with minikv-backed durability.
type BM25Index struct {
	mu    sync.RWMutex
	cols  map[string]*bm25Col
	store *Store
}

// NewBM25Index constructs an empty index bound to a Store for persistence.
func NewBM25Index(store *Store) *BM25Index {
	return &BM25Index{cols: make(map[string]*bm25Col), store: store}
}

func (b *BM25Index) col(col string) *bm25Col {
	c, ok := b.cols[col]
	if !ok {
		c = newBM25Col()
		b.cols[col] = c
	}
	return c
}

// Size returns total posting entries across all collections (observability).
func (b *BM25Index) Size() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, c := range b.cols {
		for _, m := range c.postings {
			n += len(m)
		}
	}
	return n
}

// Stats returns (uniqueTerms, indexedChunks) of a collection.
func (b *BM25Index) Stats(col string) (terms, chunks int) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	c, ok := b.cols[col]
	if !ok {
		return 0, 0
	}
	return len(c.postings), c.chunkCount
}

// ---- 分词 ----

// tokenizeBM25 splits text into BM25 terms: lowercase ASCII words (len>=2)
// and CJK bigrams (single-char CJK runs kept as unigrams).
func tokenizeBM25(text string) []string {
	text = strings.ToLower(text)
	out := make([]string, 0, 32)
	flushLatin := func(b strings.Builder) {
		s := b.String()
		if len(s) >= 2 {
			out = append(out, s)
		}
	}
	var latin strings.Builder
	var cjk []rune
	flushCJK := func() {
		if len(cjk) == 0 {
			return
		}
		if len(cjk) == 1 {
			out = append(out, string(cjk))
			cjk = cjk[:0]
			return
		}
		for i := 0; i+1 < len(cjk); i++ {
			out = append(out, string(cjk[i:i+2]))
		}
		cjk = cjk[:0]
	}
	for _, r := range text {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			flushCJK()
			latin.WriteRune(r)
		case unicode.Is(unicode.Han, r):
			flushLatin(latin)
			latin.Reset()
			cjk = append(cjk, r)
		default:
			flushLatin(latin)
			latin.Reset()
			flushCJK()
		}
	}
	flushLatin(latin)
	flushCJK()
	return out
}

// ---- 入库 / 删除 ----

// Add indexes all chunks of one document (idempotent) and persists postings.
func (b *BM25Index) Add(col, docID string, chunks []ChunkRecord) error {
	if col == "" || docID == "" || len(chunks) == 0 {
		return nil
	}
	// 1) 内存合并 (per chunk: term→tf, len)
	type chunkTerms struct {
		cid  string
		tf   map[string]int
		size int
	}
	batch := make([]chunkTerms, 0, len(chunks))
	for _, c := range chunks {
		cid := chunkID(c.Col, c.DocID, c.Seq)
		tf := make(map[string]int)
		for _, t := range tokenizeBM25(c.Text) {
			tf[t]++
		}
		batch = append(batch, chunkTerms{cid: cid, tf: tf, size: len(tf)})
	}

	b.mu.Lock()
	cc := b.col(col)
	dirtyTerms := make(map[string]struct{}) // 受影响 term → 需重写持久化
	for _, ct := range batch {
		// 幂等: 同 chunkID 重复 Add 先清旧贡献
		if old, ok := cc.chunkLen[ct.cid]; ok {
			b.removeChunkLocked(cc, ct.cid, old, dirtyTerms)
		}
		cc.chunkLen[ct.cid] = ct.size
		cc.chunkCount++
		cc.totalLen += ct.size
		for term, n := range ct.tf {
			if cc.postings[term] == nil {
				cc.postings[term] = make(map[string]bm25Posting)
			}
			cc.postings[term][ct.cid] = bm25Posting{TF: n, Len: ct.size}
			dirtyTerms[term] = struct{}{}
			dt := cc.docTerms[docID]
			if dt == nil {
				dt = make(map[string]struct{})
				cc.docTerms[docID] = dt
			}
			dt[term] = struct{}{}
		}
	}
	// 快照要持久化的 posting 状态 (锁内拷贝, 锁外写 minikv)
	snap := make(map[string]bm25TermDoc, len(dirtyTerms))
	for term := range dirtyTerms {
		p := make(map[string]bm25Posting, len(cc.postings[term]))
		for cid, po := range cc.postings[term] {
			p[cid] = po
		}
		snap[term] = bm25TermDoc{Postings: p}
	}
	docTerms := make([]string, 0, len(cc.docTerms[docID]))
	for t := range cc.docTerms[docID] {
		docTerms = append(docTerms, t)
	}
	meta := bm25MetaDoc{ChunkCount: cc.chunkCount, TotalLen: cc.totalLen}
	b.mu.Unlock()

	return b.persist(col, docID, snap, docTerms, meta)
}

// removeChunkLocked undoes one chunk's contribution in memory; records dirty terms.
func (b *BM25Index) removeChunkLocked(cc *bm25Col, cid string, oldLen int, dirty map[string]struct{}) {
	for term, m := range cc.postings {
		if _, ok := m[cid]; ok {
			delete(m, cid)
			dirty[term] = struct{}{}
			if len(m) == 0 {
				delete(cc.postings, term)
			}
		}
	}
	if _, ok := cc.chunkLen[cid]; ok {
		delete(cc.chunkLen, cid)
		cc.chunkCount--
		cc.totalLen -= oldLen
	}
}

// RemoveDoc drops a document's chunks from index and rewrites affected postings.
func (b *BM25Index) RemoveDoc(col, docID string) error {
	if col == "" || docID == "" {
		return nil
	}
	b.mu.Lock()
	cc, ok := b.cols[col]
	if !ok {
		b.mu.Unlock()
		return nil
	}
	dirty := make(map[string]struct{})
	for cid, l := range cc.chunkLen {
		if strings.HasPrefix(cid, col+"/"+docID+"/") {
			b.removeChunkLocked(cc, cid, l, dirty)
		}
	}
	delete(cc.docTerms, docID)
	snap := make(map[string]bm25TermDoc, len(dirty))
	for term := range dirty {
		if m, ok := cc.postings[term]; ok {
			p := make(map[string]bm25Posting, len(m))
			for cid, po := range m {
				p[cid] = po
			}
			snap[term] = bm25TermDoc{Postings: p}
		} else {
			snap[term] = bm25TermDoc{} // 空 → 持久化层删除该 term key
		}
	}
	meta := bm25MetaDoc{ChunkCount: cc.chunkCount, TotalLen: cc.totalLen}
	b.mu.Unlock()

	return b.persist(col, docID, snap, nil, meta)
}

// persist writes dirty posting lists + doc term list + meta via one WriteBatch.
func (b *BM25Index) persist(col, docID string, snap map[string]bm25TermDoc, docTerms []string, meta bm25MetaDoc) error {
	if b.store == nil {
		return nil
	}
	type putOp = struct {
		key string
		val string
		del bool
	}
	ops := make([]putOp, 0, len(snap)+2)
	for term, td := range snap {
		key := bm25TermKey(col, term)
		if len(td.Postings) == 0 {
			ops = append(ops, putOp{key: key, del: true})
			continue
		}
		buf, err := json.Marshal(td)
		if err != nil {
			return fmt.Errorf("bm25 persist term %s: %w", term, err)
		}
		ops = append(ops, putOp{key: key, val: string(buf)})
	}
	if docTerms != nil {
		buf, err := json.Marshal(docTerms)
		if err != nil {
			return err
		}
		ops = append(ops, putOp{key: bm25DocKey(col, docID), val: string(buf)})
	} else {
		// RemoveDoc: doc term list 一并删除
		ops = append(ops, putOp{key: bm25DocKey(col, docID), del: true})
	}
	if buf, err := json.Marshal(meta); err == nil {
		ops = append(ops, putOp{key: bm25MetaKey(col), val: string(buf)})
	}
	batch := make([]data.BatchOp, 0, len(ops))
	for _, op := range ops {
		if op.del {
			batch = append(batch, data.BatchOp{Key: op.key, Put: false})
			continue
		}
		batch = append(batch, data.BatchOp{Key: op.key, Value: op.val, Put: true})
	}
	return b.store.WriteBatch(batch)
}

// ---- 查询 ----

// Search returns topK chunks ranked by BM25 for the query. Deterministic on ties.
func (b *BM25Index) Search(col, query string, topK int) []Hit {
	if topK <= 0 {
		topK = 5
	}
	terms := tokenizeBM25(query)
	if len(terms) == 0 {
		return nil
	}
	uniq := make(map[string]struct{}, len(terms))
	for _, t := range terms {
		uniq[t] = struct{}{}
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	cc, ok := b.cols[col]
	if !ok || cc.chunkCount == 0 {
		return nil
	}
	n := float64(cc.chunkCount)
	avgLen := float64(cc.totalLen) / n
	scores := make(map[string]float32, topK*2)
	for term := range uniq {
		m := cc.postings[term]
		if len(m) == 0 {
			continue
		}
		df := float64(len(m))
		idf := math.Log1p((n - df + 0.5) / (df + 0.5))
		for cid, p := range m {
			tf := float64(p.TF)
			norm := tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*float64(p.Len)/avgLen))
			scores[cid] += float32(idf * norm)
		}
	}
	out := make([]Hit, 0, len(scores))
	for cid, s := range scores {
		out = append(out, Hit{ChunkID: cid, Score: s})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].ChunkID < out[j].ChunkID
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > topK {
		out = out[:topK]
	}
	return out
}

// ---- 重启恢复 ----

// LoadFromStore rebuilds the in-memory index by scanning rag:bm25: keys.
// Returns collections restored.
func (b *BM25Index) LoadFromStore() (int, error) {
	if b.store == nil {
		return 0, nil
	}
	start, end := prefixRange("rag:bm25:")
	pairs, err := b.store.Scan(start, end)
	if err != nil {
		return 0, fmt.Errorf("bm25 load scan: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	restored := 0
	seenCol := make(map[string]struct{})
	for _, p := range pairs {
		rest := strings.TrimPrefix(p.Key, "rag:bm25:")
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) != 2 {
			continue
		}
		col, tail := parts[0], parts[1]
		cc := b.colLocked(col)
		switch {
		case strings.HasPrefix(tail, "t:"):
			term := strings.TrimPrefix(tail, "t:")
			var td bm25TermDoc
			if json.Unmarshal([]byte(p.Value), &td) != nil {
				continue
			}
			m := make(map[string]bm25Posting, len(td.Postings))
			for cid, po := range td.Postings {
				m[cid] = po
				cc.chunkLen[cid] = po.Len
				// docTerms 恢复
				c2, d, _, ok := parseChunkID(cid)
				if ok && c2 == col {
					dt := cc.docTerms[d]
					if dt == nil {
						dt = make(map[string]struct{})
						cc.docTerms[d] = dt
					}
					dt[term] = struct{}{}
				}
			}
			cc.postings[term] = m
			seenCol[col] = struct{}{}
		case strings.HasPrefix(tail, "meta"):
			var md bm25MetaDoc
			if json.Unmarshal([]byte(p.Value), &md) != nil {
				continue
			}
			cc.chunkCount = md.ChunkCount
			cc.totalLen = md.TotalLen
		}
	}
	// meta 丢失时从 postings 推导计数 (自愈)
	for col := range seenCol {
		cc := b.colLocked(col)
		if cc.chunkCount == 0 {
			cc.chunkCount = len(cc.chunkLen)
			for _, l := range cc.chunkLen {
				cc.totalLen += l
			}
		}
		restored++
	}
	return restored, nil
}

func (b *BM25Index) colLocked(col string) *bm25Col {
	c, ok := b.cols[col]
	if !ok {
		c = newBM25Col()
		b.cols[col] = c
	}
	return c
}

// MaybeRebuildFromChunks reindexes from rag:chunk:* when postings are absent
// (pre-M1 legacy data or lost postings), mirroring maybeRebuildIfEmpty.
func (b *BM25Index) MaybeRebuildFromChunks() {
	if b.store == nil {
		return
	}
	if b.Size() > 0 {
		return
	}
	start, end := prefixRange("rag:chunk:")
	pairs, err := b.store.Scan(start, end)
	if err != nil {
		log.Printf("[rag] bm25 rebuild scan failed: %v", err)
		return
	}
	byDoc := make(map[string]map[string]ChunkRecord) // col:docID → chunkID→chunk
	for _, p := range pairs {
		var c ChunkRecord
		if json.Unmarshal([]byte(p.Value), &c) != nil || c.Col == "" || c.DocID == "" {
			continue
		}
		key := c.Col + ":" + c.DocID
		if byDoc[key] == nil {
			byDoc[key] = make(map[string]ChunkRecord)
		}
		byDoc[key][c.ChunkID] = c
	}
	n := 0
	for k, m := range byDoc {
		parts := strings.SplitN(k, ":", 2)
		if len(parts) != 2 {
			continue
		}
		chunks := make([]ChunkRecord, 0, len(m))
		for _, c := range m {
			chunks = append(chunks, c)
		}
		sort.Slice(chunks, func(i, j int) bool { return chunks[i].Seq < chunks[j].Seq })
		if err := b.Add(parts[0], parts[1], chunks); err != nil {
			log.Printf("[rag] bm25 rebuild %s: %v", k, err)
			continue
		}
		n += len(chunks)
	}
	if n > 0 {
		log.Printf("[rag] bm25 rebuilt %d chunks from rag:chunk:* (postings were empty)", n)
	}
}

// ---- key 编码 ----

func bm25TermKey(col, term string) string {
	return fmt.Sprintf("rag:bm25:%s:t:%s", col, term)
}

func bm25MetaKey(col string) string {
	return fmt.Sprintf("rag:bm25:%s:meta", col)
}

func bm25DocKey(col, docID string) string {
	return fmt.Sprintf("rag:bm25:%s:d:%s", col, docID)
}
