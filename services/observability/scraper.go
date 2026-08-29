package observability

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ServiceURLs holds sibling service base URLs for health/metrics scrape.
type ServiceURLs struct {
	Data    string
	Meta    string
	Auth    string
	Gateway string
	Rag     string
	MiniKV  string
}

// scrapeState tracks previous Prometheus counter totals for rate = Δ/Δt.
type scrapeState struct {
	lastCounters float64
	lastAt       time.Time
}

func LoadServiceURLs() ServiceURLs {
	return ServiceURLs{
		Data:    envOr("DATA_SERVICE_URL", "http://127.0.0.1:8081"),
		Meta:    envOr("META_SERVICE_URL", "http://127.0.0.1:8083"),
		Auth:    envOr("AUTH_SERVICE_URL", "http://127.0.0.1:8082"),
		Gateway: envOr("GATEWAY_URL", "http://127.0.0.1:18080"),
		Rag:     envOr("RAG_SERVICE_URL", "http://127.0.0.1:8085"),
		MiniKV:  envOr("MINIKV_METRICS_URL", "http://127.0.0.1:9091"),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return strings.TrimRight(v, "/")
	}
	return def
}

func probeHealth(client *http.Client, url string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "down"
	}
	resp, err := client.Do(req)
	if err != nil {
		return "down"
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return "ok"
	}
	return "degraded"
}

func scrapeMetrics(client *http.Client, metricsURL string) (counters float64, avgMs float64, approx bool, ok bool) {
	resp, err := client.Get(metricsURL)
	if err != nil {
		return 0, 0, false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, false, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, false, false
	}
	text := string(body)
	counters = sumPromCounter(text, "titankv_http_requests_total")
	avgMs, approx, latOK := approxLatencyMs(text, "titankv_http_request_duration_seconds")
	if !latOK {
		avgMs, approx = 0, false
	}
	return counters, avgMs, approx, true
}

func sumPromCounter(body, prefix string) float64 {
	var total float64
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") || !strings.HasPrefix(line, prefix) {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		if v, err := strconv.ParseFloat(parts[len(parts)-1], 64); err == nil {
			total += v
		}
	}
	return total
}

// counterDeltaRate returns requests/sec from cumulative counter delta.
func counterDeltaRate(current, previous float64, elapsed time.Duration) float64 {
	if elapsed <= 0 || previous <= 0 {
		return 0
	}
	delta := current - previous
	if delta < 0 {
		return 0
	}
	return delta / elapsed.Seconds()
}

// approxLatencyMs returns average latency in ms from Prometheus summary/histogram sum+count.
// It is NOT a true P50/P99 — callers must set LatencyApprox=true.
func approxLatencyMs(body, name string) (avgMs float64, approx bool, ok bool) {
	var sum, count float64
	var haveSum, haveCount bool
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, name+"_sum ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				sum, _ = strconv.ParseFloat(parts[1], 64)
				haveSum = true
			}
		}
		if strings.HasPrefix(line, name+"_count ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				count, _ = strconv.ParseFloat(parts[1], 64)
				haveCount = true
			}
		}
	}
	if !haveSum || !haveCount || count <= 0 {
		return 0, false, false
	}
	return (sum / count) * 1000, true, true
}

func scrapeEngineMetrics(client *http.Client, metricsURL string) (activity float64, ok bool) {
	resp, err := client.Get(metricsURL)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, false
	}
	text := string(body)
	activity = sumPromCounter(text, "titankv_engine_puts_total") +
		sumPromCounter(text, "titankv_engine_gets_total")
	return activity, true
}

func buildSnapshot(urls ServiceURLs, state *scrapeState) (Metrics, map[string]string) {
	return buildSnapshotWithClient(&http.Client{Timeout: 2 * time.Second}, urls, state)
}

func buildSnapshotWithClient(client *http.Client, urls ServiceURLs, state *scrapeState) (Metrics, map[string]string) {
	deps := map[string]string{
		"data-service":   probeHealth(client, urls.Data+"/healthz"),
		"meta-service":   probeHealth(client, urls.Meta+"/healthz"),
		"auth-service":   probeHealth(client, urls.Auth+"/healthz"),
		"gateway":        probeHealth(client, urls.Gateway+"/healthz"),
		"rag-service":    probeHealth(client, urls.Rag+"/healthz"),
		"minikv-engine":  probeHealth(client, urls.MiniKV+"/healthz"),
		"storage-engine": "unknown",
	}

	if hr, err := fetchJSON(client, urls.Data+"/healthz"); err == nil {
		if b, ok := hr["backend"].(string); ok {
			deps["storage-engine"] = b
		}
	}

	var totalCounters, avgSum float64
	var latN int
	var latencyApprox bool
	var scraped bool
	for _, base := range []string{urls.Data, urls.Gateway, urls.Rag} {
		c, avg, approx, ok := scrapeMetrics(client, base+"/metrics")
		if !ok {
			continue
		}
		scraped = true
		totalCounters += c
		if avg > 0 || approx {
			avgSum += avg
			latN++
			if approx {
				latencyApprox = true
			}
		}
	}
	if eng, ok := scrapeEngineMetrics(client, urls.MiniKV+"/metrics"); ok {
		scraped = true
		totalCounters += eng
	}

	var p50 float64
	if latN > 0 {
		p50 = avgSum / float64(latN)
	}

	now := time.Now()
	qps := 0.0
	qpsSource := "none"
	if state != nil && scraped {
		elapsed := now.Sub(state.lastAt)
		if !state.lastAt.IsZero() {
			qps = counterDeltaRate(totalCounters, state.lastCounters, elapsed)
			if state.lastCounters > 0 || totalCounters > 0 {
				qpsSource = "prometheus_delta"
			}
		}
		state.lastCounters = totalCounters
		state.lastAt = now
	}

	// Honest defaults: do NOT invent storage_gb=0.01 or Raft leader_count=1.
	dataBackend := deps["storage-engine"]
	if dataBackend == "" {
		dataBackend = "unknown"
	}
	m := Metrics{
		QPS:           qps,
		P50LatencyMs:  p50,
		P99LatencyMs:  0, // unknown without histogram quantiles — never avg*2.5
		StorageGB:     0,
		StorageKnown:  false,
		NodeCount:     1, // single-process demo topology
		LeaderCount:   0, // Raft is teaching-only; not on the prod data path
		Timestamp:     now.Unix(),
		QPSSource:     qpsSource,
		LatencyApprox: latencyApprox,
		DataBackend:   dataBackend,
	}
	return m, deps
}

func fetchJSON(client *http.Client, url string) (map[string]any, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func rollupStatus(deps map[string]string) string {
	st := "ok"
	for _, v := range deps {
		if v == "down" {
			return "degraded"
		}
		if v == "degraded" {
			st = "degraded"
		}
	}
	return st
}

func formatDeps(deps map[string]string) map[string]string {
	out := make(map[string]string, len(deps))
	for k, v := range deps {
		out[k] = v
	}
	return out
}
