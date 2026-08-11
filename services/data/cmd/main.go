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

	"github.com/titan-kv/titan/services/data"
)

func main() {
	addr := getenv("DATA_ADDR", ":8081")

	store, err := data.NewStoreFromEnv()
	if err != nil {
		log.Fatalf("store init: %v", err)
	}
	defer store.Close()
	log.Printf("[INFO] data backend=%s MINIKV_ADDR=%q", store.Backend(), os.Getenv("MINIKV_ADDR"))

	svc := data.NewService(store)

	r := gin.New()
	r.Use(gin.Recovery())
	svc.RegisterRoutes(r)

	srv := &http.Server{Addr: addr, Handler: r}

	go func() {
		log.Printf("[INFO] data service listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("[INFO] data service shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
