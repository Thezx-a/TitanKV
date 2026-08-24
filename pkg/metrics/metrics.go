// Package metrics provides Prometheus instrumentation shared by TitanKV Go services.
package metrics

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
)

var (
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "titankv_http_requests_total",
		Help: "Total HTTP requests by service, method, path pattern, status.",
	}, []string{"service", "method", "path", "status"})

	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "titankv_http_request_duration_seconds",
		Help:    "HTTP request latency in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"service", "method", "path"})

	KVOperationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "titankv_kv_operations_total",
		Help: "KV operations by backend and op.",
	}, []string{"backend", "op", "result"})
)

// GinMiddleware records request counts and latency for a named service.
func GinMiddleware(service string) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		c.Next()
		status := strconv.Itoa(c.Writer.Status())
		HTTPRequestsTotal.WithLabelValues(service, c.Request.Method, path, status).Inc()
		HTTPRequestDuration.WithLabelValues(service, c.Request.Method, path).Observe(time.Since(start).Seconds())
	}
}

// RegisterRoutes mounts /metrics on the given Gin engine.
func RegisterRoutes(r *gin.Engine) {
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
}

// HTTPHandler returns a standalone Prometheus handler.
func HTTPHandler() http.Handler {
	return promhttp.Handler()
}

// ObserveKV records a KV operation result.
func ObserveKV(backend, op, result string) {
	KVOperationsTotal.WithLabelValues(backend, op, result).Inc()
}
