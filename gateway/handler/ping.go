package handler

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Ping 简单健康检查, 验证中间件链.
// GET /ping → 200 {"pong": true, "request_id": ...}
func Ping(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"pong":       true,
		"request_id": c.GetString("request_id"),
	})
}

// HealthProbes configures real dependency probes for /healthz (Phase O).
// Empty Targets keeps process-liveness-only behavior.
type HealthProbes struct {
	Client  *http.Client
	Targets map[string]string // name → absolute healthz URL
}

// Healthz 健康检查.
// GET /healthz → 200 {"status","version","deps"}.
// status 为 ok|degraded：对 Targets 做真实 HTTP probe，禁止无探测硬编码全 ok。
func Healthz(version string, probes ...HealthProbes) gin.HandlerFunc {
	var p HealthProbes
	if len(probes) > 0 {
		p = probes[0]
	}
	if p.Client == nil {
		p.Client = &http.Client{Timeout: 1500 * time.Millisecond}
	}
	return func(c *gin.Context) {
		deps := map[string]string{}
		status := "ok"
		for name, url := range p.Targets {
			st := probeOne(p.Client, url)
			deps[name] = st
			if st != "ok" {
				status = "degraded"
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"status":  status,
			"version": version,
			"deps":    deps,
		})
	}
}

func probeOne(client *http.Client, url string) string {
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
