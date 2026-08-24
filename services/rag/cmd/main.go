// TitanKV RAG service entry.
// Start: go run ./services/rag/cmd
// Port: 8085 (default, RAG_ADDR / RAG_SERVICE_PORT)
//
// 依赖: MINIKV_ADDR 指向已运行的 minikv_server (默认 127.0.0.1:8888).
// 无 OPENAI_* 时自动降级为 local-mock (hash embedding + 流式 mock), 保证可独立运行.
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

	"github.com/titan-kv/titan/pkg/metrics"
	"github.com/titan-kv/titan/services/rag"
)

func main() {
	cfg := rag.LoadConfig()
	svc, err := rag.NewService(cfg)
	if err != nil {
		log.Fatalf("[FATAL] rag service init: %v", err)
	}
	defer svc.Close()

	r := gin.New()
	r.Use(gin.Recovery(), metrics.GinMiddleware("rag"))
	metrics.RegisterRoutes(r)
	svc.RegisterRoutes(r)

	srv := &http.Server{Addr: cfg.Addr, Handler: r}
	go func() {
		log.Printf("[INFO] rag service listening on %s (minikv=%s embedding=%s chat=%s)",
			cfg.Addr, cfg.MinikvAddr, cfg.EmbeddingProvider, cfg.ChatProvider)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("[INFO] rag service shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
