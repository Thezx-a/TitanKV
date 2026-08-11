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
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/titan-kv/titan/services/meta"
)

func main() {
	addr := getenv("META_ADDR", ":8083")
	etcdAddr := getenv("ETCD_ADDR", "localhost:2379")

	store := meta.NewStore()
	svc := meta.NewService(store)

	etcdCli, err := clientv3.New(clientv3.Config{Endpoints: []string{etcdAddr}, DialTimeout: 2 * time.Second})
	if err != nil {
		log.Printf("[WARN] etcd client init failed: %v (running without watch)", err)
		etcdCli = nil
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if _, err := etcdCli.Get(ctx, "/"); err != nil {
			log.Printf("[WARN] etcd unreachable: %v (running without watch)", err)
			_ = etcdCli.Close()
			etcdCli = nil
		}
		cancel()
	}

	r := gin.New()
	r.Use(gin.Recovery())
	svc.RegisterRoutes(r)

	srv := &http.Server{Addr: addr, Handler: r}
	go func() {
		log.Printf("[INFO] meta service listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	ctxWatch, cancelWatch := context.WithCancel(context.Background())
	if etcdCli != nil {
		go svc.WatchEtcd(ctxWatch, etcdCli)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("[INFO] meta service shutting down...")
	cancelWatch()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	_ = srv.Shutdown(ctx2)
	if etcdCli != nil {
		_ = etcdCli.Close()
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
