package gateway

import (
	"hash/fnv"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// shardRouter picks a Data service URL by consistent hash on the request key.
// Env: DATA_SHARD_URLS=comma-separated URLs (e.g. http://127.0.0.1:8081,http://127.0.0.1:8082)
func shardRouter() gin.HandlerFunc {
	urls := parseShardURLs(os.Getenv("DATA_SHARD_URLS"))
	if len(urls) <= 1 {
		return nil
	}
	return func(c *gin.Context) {
		key := c.Query("key")
		if key == "" {
			key = c.Param("key")
		}
		if key == "" {
			c.Next()
			return
		}
		idx := shardIndex(key, len(urls))
		c.Set("shard_target", urls[idx])
		c.Next()
	}
}

func parseShardURLs(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func shardIndex(key string, n int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % uint32(n))
}

// RegisterClusterRoutes exposes shard/raft status for the admin console.
func RegisterClusterRoutes(r *gin.Engine) {
	r.GET("/api/cluster/status", func(c *gin.Context) {
		urls := parseShardURLs(os.Getenv("DATA_SHARD_URLS"))
		c.JSON(http.StatusOK, gin.H{
			"shards":      len(urls),
			"shard_urls":  urls,
			"raft_addr":   os.Getenv("RAFT_HTTP_ADDR"),
			"description": "consistent-hash routing when DATA_SHARD_URLS set",
		})
	})
}
