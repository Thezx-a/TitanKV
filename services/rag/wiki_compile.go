package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Compiler turns ingested rag documents into wiki pages + edges.
type Compiler struct {
	wiki   *WikiStore
	store  *Store
	embed  Embedder
	index  VectorIndex
	chat   ChatProvider // optional; used when enableLLM
	enableLLM bool
}

// NewCompiler constructs a wiki compiler.
// enableLLM=false → ruleSummary only; true → ChatProvider rewrites Summary.
func NewCompiler(wiki *WikiStore, store *Store, embed Embedder, index VectorIndex, chat ChatProvider, enableLLM bool) *Compiler {
	return &Compiler{wiki: wiki, store: store, embed: embed, index: index, chat: chat, enableLLM: enableLLM}
}

// CompileDocument extracts pages from doc chunks, optionally LLM-summarizes, WriteBatch + index.
func (c *Compiler) CompileDocument(ctx context.Context, col, docID string) (*CompileTask, error) {
	return c.compileDocument(ctx, col, docID, "")
}

func (c *Compiler) compileDocument(ctx context.Context, col, docID, taskID string) (*CompileTask, error) {
	now := time.Now().Unix()
	if taskID == "" {
		taskID = uuid.NewString()
	}
	task := &CompileTask{
		TaskID: taskID, Col: col, DocID: docID,
		Status: TaskRunning, CreatedAt: now, UpdatedAt: now,
	}
	if prev, err := c.wiki.LoadCompileTask(taskID); err == nil && prev != nil {
		task.CreatedAt = prev.CreatedAt
	}
	_ = c.wiki.SaveCompileTask(task)

	chunks, err := c.store.ListChunks(col, docID)
	if err != nil {
		return c.failTask(task, err)
	}
	if len(chunks) == 0 {
		return c.failTask(task, fmt.Errorf("no chunks for %s/%s", col, docID))
	}

	md := chunksToMarkdown(chunks)
	h := sha256.Sum256([]byte(md))
	_ = c.wiki.SaveRaw(col, docID, []byte(md), hex.EncodeToString(h[:]))

	pages, edges := ExtractFromMarkdown(md, docID)
	if len(pages) == 0 {
		return c.failTask(task, fmt.Errorf("extract produced 0 pages"))
	}

	if c.enableLLM && c.chat != nil {
		for i := range pages {
			sum, err := summarizePage(ctx, c.chat, pages[i])
			if err == nil && strings.TrimSpace(sum) != "" {
				pages[i].Summary = strings.TrimSpace(sum)
			}
		}
	}

	pages, edges, err = c.wiki.ResolveContested(col, pages, edges)
	if err != nil {
		return c.failTask(task, err)
	}

	if err := c.wiki.SavePagesAndEdges(col, pages, edges); err != nil {
		return c.failTask(task, err)
	}

	// embed summaries into SideIndex under col/wiki/{slug}
	for _, p := range pages {
		text := p.Summary
		if text == "" {
			text = p.Frontmatter.Title + "\n" + p.Body
		}
		vec, err := c.embed.Embed(ctx, text)
		if err != nil {
			continue
		}
		c.index.Add(wikiChunkID(col, p.Frontmatter.Slug), vec)
	}

	task.Status = TaskSuccess
	task.Pages = len(pages)
	task.Edges = len(edges)
	task.UpdatedAt = time.Now().Unix()
	_ = c.wiki.SaveCompileTask(task)
	_ = c.wiki.AppendLog(col, WikiLogEntry{
		Action: "compile", Subject: docID,
		Detail: fmt.Sprintf("pages=%d edges=%d", len(pages), len(edges)),
	})
	return task, nil
}

func (c *Compiler) failTask(task *CompileTask, err error) (*CompileTask, error) {
	task.Status = TaskFailed
	task.Error = err.Error()
	task.UpdatedAt = time.Now().Unix()
	_ = c.wiki.SaveCompileTask(task)
	return task, err
}

func wikiChunkID(col, slug string) string {
	return fmt.Sprintf("%s/wiki/%s", col, slug)
}

func chunksToMarkdown(chunks []ChunkRecord) string {
	var b strings.Builder
	for i, c := range chunks {
		h := strings.TrimSpace(c.Heading)
		if h != "" {
			if i > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString("# ")
			b.WriteString(h)
			b.WriteString("\n\n")
		} else if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimSpace(c.Text))
	}
	return b.String()
}

func summarizePage(ctx context.Context, chat ChatProvider, page WikiPage) (string, error) {
	prompt := fmt.Sprintf(
		"用一两句中文概括下面 wiki 页，只输出摘要正文，不要标题。\n标题: %s\n\n%s",
		page.Frontmatter.Title, page.Body,
	)
	var b strings.Builder
	err := chat.StreamComplete(ctx, prompt, func(tok string) error {
		b.WriteString(tok)
		return nil
	})
	return b.String(), err
}

// ---- async compile pool ----

type CompilePoolConfig struct {
	Workers   int
	QueueSize int
}

type compileJob struct {
	Col    string
	DocID  string
	TaskID string
}

// CompilePool runs CompileDocument jobs in the background.
type CompilePool struct {
	queue  chan compileJob
	c      *Compiler
	done   chan struct{}
}

// NewCompilePool starts workers. Safe to Close().
func NewCompilePool(cfg CompilePoolConfig, c *Compiler) *CompilePool {
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 64
	}
	p := &CompilePool{
		queue: make(chan compileJob, cfg.QueueSize),
		c:     c,
		done:  make(chan struct{}),
	}
	for i := 0; i < cfg.Workers; i++ {
		go p.loop()
	}
	return p
}

func (p *CompilePool) loop() {
	for job := range p.queue {
		ctx := context.Background()
		_, _ = p.c.compileDocument(ctx, job.Col, job.DocID, job.TaskID)
	}
}

// Enqueue schedules compile; returns task_id immediately.
func (p *CompilePool) Enqueue(col, docID string) (string, error) {
	taskID := uuid.NewString()
	now := time.Now().Unix()
	_ = p.c.wiki.SaveCompileTask(&CompileTask{
		TaskID: taskID, Col: col, DocID: docID,
		Status: TaskPending, CreatedAt: now, UpdatedAt: now,
	})
	select {
	case p.queue <- compileJob{Col: col, DocID: docID, TaskID: taskID}:
		return taskID, nil
	default:
		return taskID, ErrIngestQueueFull
	}
}

// Close stops accepting jobs and drains workers.
func (p *CompilePool) Close() {
	close(p.queue)
}
