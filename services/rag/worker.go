package rag

import (
	"context"
	"errors"
	"sync"
)

var ErrIngestQueueFull = errors.New("ingest queue full")

type ingestJob struct {
	Col    string
	DocID  string
	Title  string
	Source string
	Text   string
	TaskID string
}

// IngestPoolConfig configures the async ingest worker pool.
type IngestPoolConfig struct {
	Workers   int
	QueueSize int
}

// IngestPool runs ingest jobs in the background.
type IngestPool struct {
	queue     chan ingestJob
	wg        sync.WaitGroup
	closed    chan struct{}
	closeOnce sync.Once
	handle    func(context.Context, ingestJob)
}

// NewIngestPool starts workers. handle must not be nil.
func NewIngestPool(cfg IngestPoolConfig, handle func(context.Context, ingestJob)) *IngestPool {
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 256
	}
	if handle == nil {
		handle = func(context.Context, ingestJob) {}
	}
	p := &IngestPool{
		queue:  make(chan ingestJob, cfg.QueueSize),
		closed: make(chan struct{}),
		handle: handle,
	}
	for i := 0; i < cfg.Workers; i++ {
		p.wg.Add(1)
		go p.loop()
	}
	return p
}

func (p *IngestPool) loop() {
	defer p.wg.Done()
	for {
		select {
		case <-p.closed:
			return
		case job, ok := <-p.queue:
			if !ok {
				return
			}
			p.handle(context.Background(), job)
		}
	}
}

// Enqueue submits a job. Returns ErrIngestQueueFull without blocking when full.
func (p *IngestPool) Enqueue(job ingestJob) error {
	select {
	case <-p.closed:
		return errors.New("ingest pool closed")
	case p.queue <- job:
		return nil
	default:
		return ErrIngestQueueFull
	}
}

// Close signals workers to stop and waits for them.
func (p *IngestPool) Close() {
	p.closeOnce.Do(func() {
		close(p.closed)
	})
	p.wg.Wait()
}
