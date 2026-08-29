package handler_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/titan-kv/titan/gateway/handler"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestHealthzProbesDeps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "data:8081":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"ok"}`)), Header: make(http.Header)}, nil
		case "rag:8085":
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("down")), Header: make(http.Header)}, nil
		default:
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		}
	})}

	r := gin.New()
	r.GET("/healthz", handler.Healthz("0.1.0", handler.HealthProbes{
		Client: client,
		Targets: map[string]string{
			"data": "http://data:8081/healthz",
			"rag":  "http://rag:8085/healthz",
		},
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		// degraded still 200 for liveness; status field tells truth
		t.Fatalf("status code = %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "degraded" {
		t.Fatalf("status = %v, want degraded", body["status"])
	}
	deps, _ := body["deps"].(map[string]any)
	if deps["data"] != "ok" || deps["rag"] != "degraded" && deps["rag"] != "down" {
		t.Fatalf("deps = %#v", deps)
	}
}

func TestHealthzAllOk(t *testing.T) {
	gin.SetMode(gin.TestMode)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"ok"}`)), Header: make(http.Header)}, nil
	})}
	r := gin.New()
	r.GET("/healthz", handler.Healthz("0.1.0", handler.HealthProbes{
		Client:  client,
		Targets: map[string]string{"data": "http://data/healthz", "rag": "http://rag/healthz"},
	}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "ok" {
		t.Fatalf("status = %v", body["status"])
	}
}
