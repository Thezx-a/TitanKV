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
	state       *scrapeState
}

// Metrics pushed to the admin console.
// Honesty (Phase O):
//   - QPS comes from Prometheus counter Δ/Δt (qps_source=prometheus_delta), never a canned number.
//   - p50_ms is average latency when only sum/count exist; latency_approx=true.
//   - p99_ms stays 0 until real histogram quantiles exist (no avg*2.5 fiction).
//   - storage_gb unknown → 0 + storage_known=false (no hardcoded 0.01).
//   - leader_count=0: Raft is teaching-only, not the production data path.
type Metrics struct {
	QPS           float64 `json:"qps"`
	P50LatencyMs  float64 `json:"p50_ms"`
	P99LatencyMs  float64 `json:"p99_ms"`
	StorageGB     float64 `json:"storage_gb"`
	StorageKnown  bool    `json:"storage_known"`
	NodeCount     int     `json:"node_count"`
	LeaderCount   int     `json:"leader_count"`
	Timestamp     int64   `json:"timestamp"`
	QPSSource     string  `json:"qps_source"`
	LatencyApprox bool    `json:"latency_approx"`
	// DataBackend: "minikv" | "memory" | "unknown" — Phase F yellow-bar signal.
	DataBackend string `json:"data_backend"`
}

// NewService scrapes real Prometheus /metrics from sibling services.
func NewService() *Service {
	urls := LoadServiceURLs()
	s := &Service{
		subscribers: make(map[chan Metrics]struct{}),
		urls:        urls,
		state:       &scrapeState{},
	}
	m, deps := buildSnapshot(urls, s.state)
	s.current = m
	s.lastDeps = deps
	go s.collectLoop()
	return s
}

func (s *Service) collectLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		m, deps := buildSnapshot(s.urls, s.state)
		s.mu.Lock()
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
		_, deps = buildSnapshot(s.urls, s.state)
	}
	return HealthStatus{
		Status:  rollupStatus(deps),
		Version: "0.2.0",
		Uptime:  int64(time.Since(startTime).Seconds()),
		Deps:    formatDeps(deps),
	}
}

var startTime = time.Now()
