package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/titan-kv/titan/services/data"
)

// Wiki key space (design: titanwiki-and-full-roadmap.md §6):
//
//	wiki:raw:{col}:{src_id}
//	wiki:page:{col}:{slug}
//	wiki:edge:{col}:{from}:{to}
//	wiki:index:{col}
//	wiki:log:{col}:{ts}:{id}
//	wiki:task:{task_id}

func wikiRawKey(col, srcID string) string {
	return fmt.Sprintf("wiki:raw:%s:%s", col, srcID)
}

func wikiPageKey(col, slug string) string {
	return fmt.Sprintf("wiki:page:%s:%s", col, slug)
}

func wikiPagePrefix(col string) string {
	return fmt.Sprintf("wiki:page:%s:", col)
}

func wikiEdgeKey(col, from, to string) string {
	return fmt.Sprintf("wiki:edge:%s:%s:%s", col, from, to)
}

func wikiEdgePrefix(col string) string {
	return fmt.Sprintf("wiki:edge:%s:", col)
}

func wikiEdgeFromPrefix(col, from string) string {
	return fmt.Sprintf("wiki:edge:%s:%s:", col, from)
}

func wikiIndexKey(col string) string {
	return fmt.Sprintf("wiki:index:%s", col)
}

func wikiLogKey(col string, ts int64, id string) string {
	return fmt.Sprintf("wiki:log:%s:%d:%s", col, ts, id)
}

func wikiTaskKey(taskID string) string {
	return fmt.Sprintf("wiki:task:%s", taskID)
}

// WikiStore persists compiled wiki pages/edges on the same Store (minikv).
type WikiStore struct {
	store        *Store
	keepVersions int // M5: 页面归档保留版本数 (0 = 默认 3)
}

// NewWikiStore wraps an existing RAG Store.
func NewWikiStore(store *Store) *WikiStore {
	return &WikiStore{store: store, keepVersions: 3}
}

// SetKeepVersions 设置页面归档保留版本数 (M5, ≤0 重置为默认 3).
func (w *WikiStore) SetKeepVersions(n int) {
	if n <= 0 {
		n = 3
	}
	w.keepVersions = n
}

// SaveRaw writes immutable source bytes + sha256 meta as JSON envelope.
func (w *WikiStore) SaveRaw(col, srcID string, body []byte, sha256hex string) error {
	payload := struct {
		Meta WikiRawMeta `json:"meta"`
		Body string      `json:"body"`
	}{
		Meta: WikiRawMeta{SrcID: srcID, SHA256: sha256hex, Bytes: len(body)},
		Body: string(body),
	}
	return w.store.PutJSON(wikiRawKey(col, srcID), payload)
}

// SavePagesAndEdges atomically writes pages, edges, and refreshes the index.
func (w *WikiStore) SavePagesAndEdges(col string, pages []WikiPage, edges []WikiEdge) error {
	now := time.Now().Unix()
	ops := make([]data.BatchOp, 0, len(pages)+len(edges)+1)

	// M5: 覆盖前归档当前 live 页 (版本化, 支撑回滚)
	for i := range pages {
		if cur, err := w.GetPage(col, pages[i].Frontmatter.Slug); err == nil && cur != nil {
			if err := w.archiveCurrentPage(col, cur, w.keepVersions); err != nil {
				return fmt.Errorf("archive %s: %w", pages[i].Frontmatter.Slug, err)
			}
		}
	}

	entries := make([]WikiIndexEntry, 0, len(pages))
	for i := range pages {
		pages[i].Frontmatter.UpdatedAt = now
		if pages[i].Frontmatter.CompileVer == 0 {
			pages[i].Frontmatter.CompileVer = 1
		}
		b, err := json.Marshal(pages[i])
		if err != nil {
			return fmt.Errorf("marshal page %s: %w", pages[i].Frontmatter.Slug, err)
		}
		ops = append(ops, data.BatchOp{
			Put: true, Key: wikiPageKey(col, pages[i].Frontmatter.Slug), Value: string(b),
		})
		entries = append(entries, WikiIndexEntry{
			Slug:    pages[i].Frontmatter.Slug,
			Title:   pages[i].Frontmatter.Title,
			Type:    pages[i].Frontmatter.Type,
			Summary: pages[i].Summary,
		})
	}
	for _, e := range edges {
		b, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("marshal edge: %w", err)
		}
		ops = append(ops, data.BatchOp{
			Put: true, Key: wikiEdgeKey(col, e.From, e.To), Value: string(b),
		})
	}

	// merge with existing index entries not overwritten by this batch
	existing, _ := w.LoadIndex(col)
	bySlug := map[string]WikiIndexEntry{}
	if existing != nil {
		for _, e := range existing.Entries {
			bySlug[e.Slug] = e
		}
	}
	for _, e := range entries {
		bySlug[e.Slug] = e
	}
	merged := make([]WikiIndexEntry, 0, len(bySlug))
	for _, e := range bySlug {
		merged = append(merged, e)
	}
	idx := WikiIndexDoc{Col: col, UpdatedAt: now, Entries: merged}
	ib, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	ops = append(ops, data.BatchOp{Put: true, Key: wikiIndexKey(col), Value: string(ib)})
	return w.store.WriteBatch(ops)
}

// GetPage loads one wiki page by slug.
func (w *WikiStore) GetPage(col, slug string) (*WikiPage, error) {
	var p WikiPage
	ok, err := w.store.GetJSON(wikiPageKey(col, slug), &p)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &p, nil
}

// ListEdges returns edges from or to slug. out=true → from==slug.
func (w *WikiStore) ListEdges(col, slug string, out bool) ([]WikiEdge, error) {
	if out {
		start, end := prefixRange(wikiEdgeFromPrefix(col, slug))
		pairs, err := w.store.Scan(start, end)
		if err != nil {
			return nil, err
		}
		edges := make([]WikiEdge, 0, len(pairs))
		for _, p := range pairs {
			var e WikiEdge
			if err := json.Unmarshal([]byte(p.Value), &e); err == nil {
				edges = append(edges, e)
			}
		}
		return edges, nil
	}
	// inbound: scan all edges in col and filter To==slug
	start, end := prefixRange(wikiEdgePrefix(col))
	pairs, err := w.store.Scan(start, end)
	if err != nil {
		return nil, err
	}
	edges := make([]WikiEdge, 0)
	for _, p := range pairs {
		var e WikiEdge
		if err := json.Unmarshal([]byte(p.Value), &e); err == nil && e.To == slug {
			edges = append(edges, e)
		}
	}
	return edges, nil
}

// LoadIndex returns the collection wiki catalog.
func (w *WikiStore) LoadIndex(col string) (*WikiIndexDoc, error) {
	var idx WikiIndexDoc
	ok, err := w.store.GetJSON(wikiIndexKey(col), &idx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &WikiIndexDoc{Col: col, Entries: nil}, nil
	}
	return &idx, nil
}

// AppendLog appends one audit log entry.
func (w *WikiStore) AppendLog(col string, entry WikiLogEntry) error {
	if entry.CreatedAt == 0 {
		entry.CreatedAt = time.Now().Unix()
	}
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	return w.store.PutJSON(wikiLogKey(col, entry.CreatedAt, id), entry)
}

// SaveCompileTask persists a compile task.
func (w *WikiStore) SaveCompileTask(t *CompileTask) error {
	t.UpdatedAt = time.Now().Unix()
	return w.store.PutJSON(wikiTaskKey(t.TaskID), t)
}

// LoadCompileTask loads a compile task.
func (w *WikiStore) LoadCompileTask(taskID string) (*CompileTask, error) {
	var t CompileTask
	ok, err := w.store.GetJSON(wikiTaskKey(taskID), &t)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("wiki task not found")
	}
	return &t, nil
}

// DeleteBySource removes pages/edges whose Sources contain docID, raw, and rebuilds index.
func (w *WikiStore) DeleteBySource(col, docID string) error {
	// pages
	start, end := prefixRange(wikiPagePrefix(col))
	pairs, err := w.store.Scan(start, end)
	if err != nil {
		return err
	}
	deleteSlugs := map[string]bool{}
	ops := make([]data.BatchOp, 0)
	for _, p := range pairs {
		var page WikiPage
		if err := json.Unmarshal([]byte(p.Value), &page); err != nil {
			continue
		}
		if !containsString(page.Frontmatter.Sources, docID) {
			continue
		}
		deleteSlugs[page.Frontmatter.Slug] = true
		ops = append(ops, data.BatchOp{Put: false, Key: p.Key})
	}

	// edges involving deleted slugs
	eStart, eEnd := prefixRange(wikiEdgePrefix(col))
	ePairs, err := w.store.Scan(eStart, eEnd)
	if err != nil {
		return err
	}
	for _, p := range ePairs {
		var e WikiEdge
		if err := json.Unmarshal([]byte(p.Value), &e); err != nil {
			continue
		}
		if deleteSlugs[e.From] || deleteSlugs[e.To] {
			ops = append(ops, data.BatchOp{Put: false, Key: p.Key})
		}
	}

	// raw
	ops = append(ops, data.BatchOp{Put: false, Key: wikiRawKey(col, docID)})

	if err := w.store.WriteBatch(ops); err != nil {
		return err
	}

	// rebuild index from remaining pages
	return w.rebuildIndex(col)
}

func (w *WikiStore) rebuildIndex(col string) error {
	start, end := prefixRange(wikiPagePrefix(col))
	pairs, err := w.store.Scan(start, end)
	if err != nil {
		return err
	}
	entries := make([]WikiIndexEntry, 0, len(pairs))
	for _, p := range pairs {
		var page WikiPage
		if err := json.Unmarshal([]byte(p.Value), &page); err != nil {
			continue
		}
		entries = append(entries, WikiIndexEntry{
			Slug: page.Frontmatter.Slug, Title: page.Frontmatter.Title,
			Type: page.Frontmatter.Type, Summary: page.Summary,
		})
	}
	idx := WikiIndexDoc{Col: col, UpdatedAt: time.Now().Unix(), Entries: entries}
	return w.store.PutJSON(wikiIndexKey(col), idx)
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func sourcesOverlap(a, b []string) bool {
	for _, x := range a {
		if containsString(b, x) {
			return true
		}
	}
	return false
}

func mergeSources(a, b []string) []string {
	out := append([]string{}, a...)
	for _, x := range b {
		if !containsString(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func contentFingerprint(p WikiPage) string {
	s := strings.TrimSpace(p.Summary)
	if s == "" {
		s = strings.TrimSpace(p.Body)
	}
	s = strings.Join(strings.Fields(s), " ")
	h := sha256.Sum256([]byte(strings.ToLower(s)))
	return hex.EncodeToString(h[:8])
}

func shortSourceID(src string) string {
	if src == "" {
		return "alt"
	}
	h := sha256.Sum256([]byte(src))
	return hex.EncodeToString(h[:4])
}

// ResolveContested adjusts pages/edges so conflicting same-slug pages from
// different sources are not overwritten: original kept + contested; new page
// written under slug-alt-<id>; contradicts edges added both ways.
func (w *WikiStore) ResolveContested(col string, pages []WikiPage, edges []WikiEdge) ([]WikiPage, []WikiEdge, error) {
	out := make([]WikiPage, 0, len(pages)+len(pages))
	extraEdges := append([]WikiEdge{}, edges...)
	slugRemap := map[string]string{} // oldSlug -> newSlug when renamed

	for _, p := range pages {
		existing, err := w.GetPage(col, p.Frontmatter.Slug)
		if err != nil {
			return nil, nil, err
		}
		if existing == nil {
			out = append(out, p)
			continue
		}
		// same/overlapping sources → safe overwrite / merge sources
		if sourcesOverlap(existing.Frontmatter.Sources, p.Frontmatter.Sources) {
			p.Frontmatter.Sources = mergeSources(existing.Frontmatter.Sources, p.Frontmatter.Sources)
			p.Frontmatter.Contested = existing.Frontmatter.Contested
			out = append(out, p)
			continue
		}
		// different sources but same fingerprint → merge sources, no contested
		if contentFingerprint(*existing) == contentFingerprint(p) {
			existing.Frontmatter.Sources = mergeSources(existing.Frontmatter.Sources, p.Frontmatter.Sources)
			out = append(out, *existing)
			continue
		}
		// CONFLICT: keep original body, mark both contested, write alt page
		existing.Frontmatter.Contested = true
		out = append(out, *existing)

		alt := p
		srcID := ""
		if len(p.Frontmatter.Sources) > 0 {
			srcID = p.Frontmatter.Sources[0]
		}
		altSlug := p.Frontmatter.Slug + "-alt-" + shortSourceID(srcID)
		slugRemap[p.Frontmatter.Slug] = altSlug
		alt.Frontmatter.Slug = altSlug
		alt.Frontmatter.Contested = true
		out = append(out, alt)

		extraEdges = append(extraEdges,
			WikiEdge{From: existing.Frontmatter.Slug, To: altSlug, Rel: "contradicts"},
			WikiEdge{From: altSlug, To: existing.Frontmatter.Slug, Rel: "contradicts"},
		)
	}

	// remap edges that pointed to renamed slugs as From (new pages' outbound links)
	for i := range extraEdges {
		if mapped, ok := slugRemap[extraEdges[i].From]; ok && extraEdges[i].Rel == "links_to" {
			extraEdges[i].From = mapped
		}
	}
	return out, extraEdges, nil
}

// ListContested returns all pages in the collection with Contested=true.
func (w *WikiStore) ListContested(col string) ([]WikiPage, error) {
	start, end := prefixRange(wikiPagePrefix(col))
	pairs, err := w.store.Scan(start, end)
	if err != nil {
		return nil, err
	}
	out := make([]WikiPage, 0)
	for _, p := range pairs {
		var page WikiPage
		if err := json.Unmarshal([]byte(p.Value), &page); err != nil {
			continue
		}
		if page.Frontmatter.Contested {
			out = append(out, page)
		}
	}
	return out, nil
}

// DeleteCollection wipes all wiki keys for a collection via DeleteRange prefixes.
// Order: pages → edges → raw → log → index (index last so catalog vanishes after data).
func (w *WikiStore) DeleteCollection(col string) error {
	prefixes := []string{
		wikiPagePrefix(col),
		wikiEdgePrefix(col),
		fmt.Sprintf("wiki:raw:%s:", col),
		fmt.Sprintf("wiki:log:%s:", col),
	}
	for _, p := range prefixes {
		if err := w.store.DeletePrefix(p); err != nil {
			return err
		}
	}
	return w.store.Delete(wikiIndexKey(col))
}

// ResolveSlugExact finds a page whose slug equals the query slug, or title slugifies to it.
func (w *WikiStore) ResolveSlugExact(col, query string) (*WikiPage, error) {
	slug := SlugifyTitle(query)
	if p, err := w.GetPage(col, slug); err != nil {
		return nil, err
	} else if p != nil {
		return p, nil
	}
	// also try raw query as slug
	if slug != query {
		if p, err := w.GetPage(col, strings.TrimSpace(query)); err != nil {
			return nil, err
		} else if p != nil {
			return p, nil
		}
	}
	return nil, nil
}

// ---- M5: 页面版本化与回滚 ----
//
// key 布局 (独立前缀, 不进 wikiPagePrefix 扫描):
//
//	wiki:pver:{col}:{slug}:head        → 当前版本号 (int)
//	wiki:pver:{col}:{slug}:{ver:06d}   → 归档的 WikiPage JSON
//
// 语义: 每次 SavePagesAndEdges 覆盖前, 先把当前 live 页归档为 head+1;
// 回滚 = 把指定历史版本写回 live (回滚动作本身不再归档, 走 log 审计);
// 归档保留 keepVersions 份, 超出按版本号从旧到新淘汰.

func wikiPageVerHeadKey(col, slug string) string {
	return fmt.Sprintf("wiki:pver:%s:%s:head", col, slug)
}

func wikiPageVerKey(col, slug string, ver int) string {
	return fmt.Sprintf("wiki:pver:%s:%s:%06d", col, slug, ver)
}

func wikiPageVerPrefix(col, slug string) string {
	return fmt.Sprintf("wiki:pver:%s:%s:", col, slug)
}

// loadPageVerHead 返回当前版本号 (0 = 无历史).
func (w *WikiStore) loadPageVerHead(col, slug string) (int, error) {
	raw, ok, err := w.store.Get(wikiPageVerHeadKey(col, slug))
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &n); err != nil {
		return 0, nil
	}
	return n, nil
}

// archiveCurrentPage 把 live 页归档为 head+1 并淘汰超限旧版.
func (w *WikiStore) archiveCurrentPage(col string, p *WikiPage, keepVersions int) error {
	if p == nil {
		return nil
	}
	head, err := w.loadPageVerHead(col, p.Frontmatter.Slug)
	if err != nil {
		return err
	}
	next := head + 1
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	ops := []data.BatchOp{
		{Put: true, Key: wikiPageVerKey(col, p.Frontmatter.Slug, next), Value: string(b)},
		{Put: true, Key: wikiPageVerHeadKey(col, p.Frontmatter.Slug), Value: fmt.Sprintf("%d", next)},
	}
	if err := w.store.WriteBatch(ops); err != nil {
		return err
	}
	// 淘汰 keepVersions 之外的旧版
	if keepVersions > 0 {
		start, end := prefixRange(wikiPageVerPrefix(col, p.Frontmatter.Slug))
		pairs, err := w.store.Scan(start, end)
		if err != nil {
			return err
		}
		minKeep := next - keepVersions
		for _, pr := range pairs {
			var ver int
			if _, err := fmt.Sscanf(pr.Key[len(wikiPageVerPrefix(col, p.Frontmatter.Slug)):], "%06d", &ver); err != nil {
				continue
			}
			if ver <= minKeep {
				if err := w.store.Delete(pr.Key); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// RollbackPage 把 slug 页回滚到指定版本 (0 = 上一版), 并刷新索引与审计日志.
func (w *WikiStore) RollbackPage(col, slug string, toVersion int) (*WikiPage, error) {
	head, err := w.loadPageVerHead(col, slug)
	if err != nil {
		return nil, err
	}
	if head == 0 {
		return nil, fmt.Errorf("no archived versions for %s/%s", col, slug)
	}
	if toVersion == 0 {
		// 0 = 上一版 = head 归档 (归档发生在覆盖前, head 即覆盖前状态)
		toVersion = head
	}
	if toVersion < 1 || toVersion > head {
		return nil, fmt.Errorf("version %d out of range (1..%d)", toVersion, head)
	}
	raw, ok, err := w.store.Get(wikiPageVerKey(col, slug, toVersion))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("version %d not found (pruned?)", toVersion)
	}
	var p WikiPage
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("unmarshal archived page: %w", err)
	}
	p.Frontmatter.UpdatedAt = time.Now().Unix()
	if err := w.store.PutJSON(wikiPageKey(col, slug), p); err != nil {
		return nil, err
	}
	// 索引条目同步 (title/summary 可能与新版不同)
	if err := w.rebuildIndex(col); err != nil {
		return nil, err
	}
	_ = w.AppendLog(col, WikiLogEntry{
		Action: "rollback", Subject: slug,
		Detail: fmt.Sprintf("head=%d restored=%d", head, toVersion),
	})
	return &p, nil
}

// ListPageVersions 返回 slug 的可用归档版本号 (升序).
func (w *WikiStore) ListPageVersions(col, slug string) ([]int, error) {
	start, end := prefixRange(wikiPageVerPrefix(col, slug))
	pairs, err := w.store.Scan(start, end)
	if err != nil {
		return nil, err
	}
	var out []int
	pfx := wikiPageVerPrefix(col, slug)
	for _, pr := range pairs {
		var ver int
		if _, err := fmt.Sscanf(pr.Key[len(pfx):], "%06d", &ver); err == nil {
			out = append(out, ver)
		}
	}
	return out, nil
}
