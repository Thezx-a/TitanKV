package observability

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSumPromCounter(t *testing.T) {
	body := `# HELP titankv_http_requests_total
# TYPE titankv_http_requests_total counter
titankv_http_requests_total{code="200",method="GET",service="data"} 10
titankv_http_requests_total{code="500",method="POST",service="data"} 2
other_metric 99
`
	got := sumPromCounter(body, "titankv_http_requests_total")
	if got != 12 {
		t.Fatalf("sumPromCounter = %v, want 12", got)
	}
}

func TestCounterDeltaRate(t *testing.T) {
	rate := counterDeltaRate(100, 40, 2*time.Second)
	if rate != 30 {
		t.Fatalf("counterDeltaRate = %v, want 30", rate)
	}
	if counterDeltaRate(50, 0, 0) != 0 {
		t.Fatal("first sample should report 0 rate")
	}
	if counterDeltaRate(10, 20, time.Second) != 0 {
		t.Fatal("counter reset / negative delta should clamp to 0")
	}
}

func TestApproxLatencyMsIsAverageNotFakeP99(t *testing.T) {
	body := `titankv_http_request_duration_seconds_sum 2.0
titankv_http_request_duration_seconds_count 100
`
	avgMs, approx, ok := approxLatencyMs(body, "titankv_http_request_duration_seconds")
	if !ok {
		t.Fatal("expected ok")
	}
	if avgMs != 20 {
		t.Fatalf("avgMs = %v, want 20", avgMs)
	}
	if !approx {
		t.Fatal("must mark latency as approximate (no true histogram quantiles)")
	}
}

// roundTripFunc lets tests avoid real sockets (WSL /mnt httptest often connection-refused).
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func jsonResp(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestBuildSnapshotNoHardcodedStorageOrLeader(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResp(503, "down"), nil
	})}
	urls := ServiceURLs{
		Data: "http://data", Meta: "http://meta", Auth: "http://auth",
		Gateway: "http://gw", Rag: "http://rag", MiniKV: "http://kv",
	}
	m, _ := buildSnapshotWithClient(client, urls, nil)
	if m.StorageGB != 0 || m.StorageKnown {
		t.Fatalf("storage must be unknown/0, got gb=%v known=%v", m.StorageGB, m.StorageKnown)
	}
	if m.LeaderCount != 0 {
		t.Fatalf("leader_count must be 0 (Raft not on prod path), got %d", m.LeaderCount)
	}
	if m.QPS < 0 {
		t.Fatal("qps must not be negative")
	}
}

func TestBuildSnapshotPrometheusDeltaQPS(t *testing.T) {
	counter := 10
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/healthz") && strings.Contains(req.URL.Host, "data"):
			return jsonResp(200, `{"status":"ok","backend":"memory"}`), nil
		case strings.HasSuffix(req.URL.Path, "/healthz"):
			return jsonResp(503, "down"), nil
		case strings.HasSuffix(req.URL.Path, "/metrics") && strings.Contains(req.URL.Host, "data"):
			body := "titankv_http_requests_total{service=\"data\"} " + itoa(counter) + "\n"
			return jsonResp(200, body), nil
		default:
			return jsonResp(503, "down"), nil
		}
	})}
	urls := ServiceURLs{
		Data: "http://data", Meta: "http://meta", Auth: "http://auth",
		Gateway: "http://gw", Rag: "http://rag", MiniKV: "http://kv",
	}
	state := &scrapeState{}
	_, _ = buildSnapshotWithClient(client, urls, state)
	counter = 70
	state.lastAt = time.Now().Add(-1 * time.Second)
	m, _ := buildSnapshotWithClient(client, urls, state)
	if m.QPSSource != "prometheus_delta" {
		t.Fatalf("qps_source = %q, want prometheus_delta", m.QPSSource)
	}
	if m.QPS < 50 || m.QPS > 70 {
		t.Fatalf("qps = %v, want ~60", m.QPS)
	}
	if m.DataBackend != "memory" {
		t.Fatalf("data_backend = %q, want memory (F1 yellow-bar signal)", m.DataBackend)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
