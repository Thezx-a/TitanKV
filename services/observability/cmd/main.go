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

	"github.com/titan-kv/titan/services/observability"
)

func main() {
	addr := getenv("OBSERV_ADDR", ":8084")
	svc := observability.NewService()

	r := gin.New()
	r.Use(gin.Recovery())
	observability.RegisterRoutes(r, svc)

	srv := &http.Server{Addr: addr, Handler: r}
	go func() {
		log.Printf("[INFO] observability service listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("[INFO] observability service shutting down...")
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	_ = srv.Shutdown(ctx2)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
