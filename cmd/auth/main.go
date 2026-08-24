// TitanKV Auth service entry (Phase 3).
// Start: go run ./cmd/auth
// Port: 8082 (default)
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/titan-kv/titan/pkg/metrics"
	"github.com/titan-kv/titan/services/auth"
)

func main() {
	addr := getenv("AUTH_ADDR", ":8082")
	jwtSecret := getenv("JWT_SECRET", "dev-secret-change-in-production")
	redisAddr := getenv("REDIS_ADDR", "localhost:6379")

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("[WARN] redis not available: %v (refresh token / jti blacklist disabled)", err)
		cancel()
		rdb = nil
	} else {
		cancel()
	}

	svc := auth.NewService(jwtSecret, rdb)

	r := gin.New()
	r.Use(gin.Recovery(), metrics.GinMiddleware("auth"))
	metrics.RegisterRoutes(r)
	svc.RegisterRoutes(r)

	// Internal API Key issue route (gateway injects auth in production).
	r.POST("/api/auth/apikey", svc.IssueAPIKey)

	srv := &http.Server{Addr: addr, Handler: r}
	go func() {
		log.Printf("[INFO] auth service listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("[INFO] auth service shutting down...")
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	srv.Shutdown(ctx2)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
