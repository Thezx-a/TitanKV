// Package observability aggregates metrics and health from sibling TitanKV services.
package observability

import (
	"sync"
	"time"
)

// Service is the Observability service.
type Service struct {
	mu          sync.RWMutex
	current     Metrics
	lastDeps    map[string]string
	subscribers map[chan Metrics]struct{}
	urls        ServiceURLs
}

// Metrics pushed to the admin console.
type Metrics struct {
	QPS          float64 `json:"qps"`
	P50LatencyMs float64 `json:"p50_ms"`
	P99LatencyMs float64 `json:"p99_ms"`
	StorageGB    float64 `json:"storage_gb"`
	NodeCount    int     `json:"node_count"`
	LeaderCount  int     `json:"leader_count"`
	Timestamp    int64   `json:"timestamp"`
}

// NewService scrapes real Prometheus /metrics from sibling services.
func NewService() *Service {
	urls := LoadServiceURLs()
	s := &Service{
		subscribers: make(map[chan Metrics]struct{}),
		urls:        urls,
	}
	m, deps := buildSnapshot(urls)
	s.current = m
	s.lastDeps = deps
	go s.collectLoop()
	return s
}

func (s *Service) collectLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	var prevQPS float64
	for range ticker.C {
		m, deps := buildSnapshot(s.urls)
		s.mu.Lock()
		if prevQPS > 0 && m.QPS > prevQPS {
			m.QPS = m.QPS - prevQPS
		}
		prevQPS = m.QPS
		if m.QPS < 0 {
			m.QPS = 0
		}
		s.current = m
		s.lastDeps = deps
		s.mu.Unlock()
		for ch := range s.subscribers {
			select {
			case ch <- m:
			default:
			}
		}
	}
}

func (s *Service) Subscribe() chan Metrics {
	ch := make(chan Metrics, 16)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

func (s *Service) Unsubscribe(ch chan Metrics) {
	s.mu.Lock()
	delete(s.subscribers, ch)
	s.mu.Unlock()
	close(ch)
}

func (s *Service) Current() Metrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

type HealthStatus struct {
	Status  string            `json:"status"`
	Version string            `json:"version"`
	Uptime  int64             `json:"uptime_seconds"`
	Deps    map[string]string `json:"deps"`
}

func (s *Service) Health() HealthStatus {
	s.mu.RLock()
	deps := s.lastDeps
	s.mu.RUnlock()
	if deps == nil {
		_, deps = buildSnapshot(s.urls)
	}
	return HealthStatus{
		Status:  rollupStatus(deps),
		Version: "0.2.0",
		Uptime:  int64(time.Since(startTime).Seconds()),
		Deps:    formatDeps(deps),
	}
}

var startTime = time.Now()
