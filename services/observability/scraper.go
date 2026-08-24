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
}

func LoadServiceURLs() ServiceURLs {
	return ServiceURLs{
		Data:    envOr("DATA_SERVICE_URL", "http://127.0.0.1:8081"),
		Meta:    envOr("META_SERVICE_URL", "http://127.0.0.1:8083"),
		Auth:    envOr("AUTH_SERVICE_URL", "http://127.0.0.1:8082"),
		Gateway: envOr("GATEWAY_URL", "http://127.0.0.1:18080"),
		Rag:     envOr("RAG_SERVICE_URL", "http://127.0.0.1:8085"),
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
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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

func scrapeMetrics(client *http.Client, metricsURL string) (qps float64, p50ms, p99ms float64, ok bool) {
	resp, err := client.Get(metricsURL)
	if err != nil {
		return 0, 0, 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, 0, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, 0, false
	}
	text := string(body)
	qps = sumPromCounter(text, "titankv_http_requests_total")
	p50ms, p99ms = approxLatencyMs(text, "titankv_http_request_duration_seconds")
	return qps, p50ms, p99ms, true
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

func approxLatencyMs(body, name string) (p50, p99 float64) {
	var sum, count float64
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, name+"_sum ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				sum, _ = strconv.ParseFloat(parts[1], 64)
			}
		}
		if strings.HasPrefix(line, name+"_count ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				count, _ = strconv.ParseFloat(parts[1], 64)
			}
		}
	}
	if count <= 0 {
		return 0, 0
	}
	avg := (sum / count) * 1000
	return avg, avg * 2.5
}

func buildSnapshot(urls ServiceURLs) (Metrics, map[string]string) {
	client := &http.Client{Timeout: 2 * time.Second}
	deps := map[string]string{
		"data-service":   probeHealth(client, urls.Data+"/healthz"),
		"meta-service":   probeHealth(client, urls.Meta+"/healthz"),
		"auth-service":   probeHealth(client, urls.Auth+"/healthz"),
		"gateway":        probeHealth(client, urls.Gateway+"/healthz"),
		"rag-service":    probeHealth(client, urls.Rag+"/healthz"),
		"storage-engine": "unknown",
	}

	if hr, err := fetchJSON(client, urls.Data+"/healthz"); err == nil {
		if b, ok := hr["backend"].(string); ok {
			deps["storage-engine"] = b
		}
	}

	var totalQPS, p50, p99 float64
	var n int
	for _, base := range []string{urls.Data, urls.Gateway, urls.Rag} {
		q, a, b, ok := scrapeMetrics(client, base+"/metrics")
		if !ok {
			continue
		}
		totalQPS += q
		p50 += a
		p99 += b
		n++
	}
	if n > 0 {
		p50 /= float64(n)
		p99 /= float64(n)
	}

	m := Metrics{
		QPS:          totalQPS / 60,
		P50LatencyMs: p50,
		P99LatencyMs: p99,
		StorageGB:    0.01,
		NodeCount:    1,
		LeaderCount:  1,
		Timestamp:    time.Now().Unix(),
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